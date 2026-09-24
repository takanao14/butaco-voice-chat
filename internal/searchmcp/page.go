package searchmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
	stdhtml "html"
)

const maxPageBytes = 1 << 20

type ReadInput struct {
	ID string `json:"id" jsonschema:"ID returned by web_search; arbitrary URLs are not accepted"`
}

type ReadOutput struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
	RetrievedAt string `json:"retrievedAt"`
	Text        string `json:"text"`
}

func (s *Service) Read(ctx context.Context, in ReadInput) (ReadOutput, error) {
	result, ok := s.lookup(in.ID)
	if !ok {
		return ReadOutput{}, errors.New("search result ID is unknown or expired")
	}
	if !validPageURL(result.url) {
		return ReadOutput{}, errors.New("search result URL is not allowed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, result.url, nil)
	if err != nil {
		return ReadOutput{}, errors.New("could not create article request")
	}
	request.Header.Set("User-Agent", "ButakoVoiceSearch/1.0")
	response, err := s.pageHTTP.Do(request)
	if err != nil {
		return ReadOutput{}, errors.New("article unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ReadOutput{}, fmt.Errorf("article returned HTTP %d", response.StatusCode)
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if !strings.HasPrefix(contentType, "text/html") && !strings.HasPrefix(contentType, "application/xhtml+xml") {
		return ReadOutput{}, errors.New("article is not HTML")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxPageBytes+1))
	if err != nil || len(data) > maxPageBytes {
		return ReadOutput{}, errors.New("article unavailable or too large")
	}
	page, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return ReadOutput{}, errors.New("invalid article HTML")
	}
	output := extractPage(page)
	if output.Title == "" {
		output.Title = truncate(result.title, 180)
	}
	output.URL = response.Request.URL.String()
	output.RetrievedAt = time.Now().UTC().Format(time.RFC3339)
	if output.Text == "" && output.Description == "" {
		return ReadOutput{}, errors.New("article text unavailable")
	}
	return output, nil
}

func newPageClient() *http.Client {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil || len(ips) == 0 {
				return nil, errors.New("article host cannot be resolved")
			}
			for _, ip := range ips {
				if !publicIP(ip.IP) {
					return nil, errors.New("article host resolves to a non-public address")
				}
			}
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
		DisableKeepAlives: true,
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 3 || !validPageURL(request.URL.String()) {
				return errors.New("article redirect not allowed")
			}
			return nil
		},
	}
}

func validPageURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && !publicIP(ip) {
		return false
	}
	return true
}

func publicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !ip.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "2001:db8::/32"} {
		if netip.MustParsePrefix(prefix).Contains(addr) {
			return false
		}
	}
	return true
}

func extractPage(root *html.Node) ReadOutput {
	var out ReadOutput
	var article, main, body *html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "article":
				if article == nil {
					article = node
				}
			case "main":
				if main == nil {
					main = node
				}
			case "body":
				body = node
			case "meta":
				name, value := attr(node, "name"), attr(node, "content")
				property := attr(node, "property")
				if name == "description" || property == "og:description" {
					if out.Description == "" {
						out.Description = truncate(value, 600)
					}
				}
				if property == "article:published_time" && out.PublishedAt == "" {
					out.PublishedAt = truncate(value, 80)
				}
			case "title":
				if out.Title == "" {
					out.Title = truncate(nodeText(node), 180)
				}
			case "script":
				if attr(node, "type") == "application/ld+json" {
					readJSONLD([]byte(nodeText(node)), &out)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	if out.Text == "" {
		for _, node := range []*html.Node{article, main} {
			if node != nil {
				out.Text = truncate(plainNodeText(node), 6000)
				if len([]rune(out.Text)) >= 100 {
					break
				}
			}
		}
	}
	if len([]rune(out.Text)) < 100 {
		out.Text = out.Description
	}
	if out.Text == "" && body != nil {
		out.Text = truncate(plainNodeText(body), 6000)
	}
	return out
}

func readJSONLD(raw []byte, out *ReadOutput) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return
	}
	var visit func(any)
	visit = func(value any) {
		switch item := value.(type) {
		case []any:
			for _, child := range item {
				visit(child)
			}
		case map[string]any:
			if graph, ok := item["@graph"]; ok {
				visit(graph)
			}
			typeName, _ := item["@type"].(string)
			if !strings.Contains(typeName, "Article") {
				return
			}
			if headline, ok := item["headline"].(string); ok && headline != "" {
				out.Title = truncate(headline, 180)
			}
			if description, ok := item["description"].(string); ok && description != "" {
				out.Description = truncate(description, 600)
			}
			if published, ok := item["datePublished"].(string); ok {
				out.PublishedAt = truncate(published, 80)
			}
			if articleBody, ok := item["articleBody"].(string); ok && articleBody != "" {
				out.Text = truncate(articleBody, 6000)
			}
		}
	}
	visit(value)
}

func attr(node *html.Node, key string) string {
	for _, item := range node.Attr {
		if item.Key == key {
			return item.Val
		}
	}
	return ""
}

func nodeText(node *html.Node) string {
	var parts []string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			parts = append(parts, node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(parts, " ")
}

func plainNodeText(node *html.Node) string {
	var parts []string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "script", "style", "nav", "header", "footer", "aside", "form":
				return
			}
		}
		if node.Type == html.TextNode {
			parts = append(parts, node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(stdhtml.UnescapeString(strings.Join(parts, " "))), " ")
}

func plainText(value string) string {
	root, err := html.Parse(strings.NewReader(value))
	if err != nil {
		return strings.Join(strings.Fields(stdhtml.UnescapeString(value)), " ")
	}
	return plainNodeText(root)
}
