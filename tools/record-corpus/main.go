// Command record-corpus fetches the URLs of testdata/corpus/urls.txt the way a
// browser would and stores the responses in a HAR file, so that the proxy can
// be tested against real content without touching the network.
//
// Bodies are stored exactly as they came over the wire, still compressed when
// the origin compressed them. This is a deliberate departure from the HAR
// convention of storing decoded bodies, because the proxy has to be tested
// with the encodings real origins use. The "comment" field of each entry says
// so.
//
// Usage: go run ./tools/record-corpus [-urls file] [-out file]
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// The headers of a current Firefox over HTTPS.
var browserHeaders = map[string]string{
	"User-Agent":      "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0",
	"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
	"Accept-Language": "en-US,en;q=0.5",
	"Accept-Encoding": "gzip, deflate, br, zstd",
}

type har struct {
	Log harLog `json:"log"`
}

type harLog struct {
	Version string     `json:"version"`
	Creator harCreator `json:"creator"`
	Entries []entry    `json:"entries"`
}

type harCreator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type entry struct {
	Started  string   `json:"startedDateTime"`
	Request  request  `json:"request"`
	Response response `json:"response"`
	Comment  string   `json:"comment"`
}

type request struct {
	Method  string      `json:"method"`
	URL     string      `json:"url"`
	Headers []nameValue `json:"headers"`
}

type response struct {
	Status      int         `json:"status"`
	StatusText  string      `json:"statusText"`
	HTTPVersion string      `json:"httpVersion"`
	Headers     []nameValue `json:"headers"`
	Content     content     `json:"content"`
}

type content struct {
	Size     int    `json:"size"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
	Encoding string `json:"encoding"`
}

type nameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func main() {
	urls := flag.String("urls", "testdata/corpus/urls.txt", "file with one URL per line")
	out := flag.String("out", "testdata/corpus/corpus.har", "HAR file to write")
	maxSize := flag.Int("max", 1<<20, "skip bodies larger than this many bytes")
	flag.Parse()

	f, err := os.Open(*urls)
	if err != nil {
		fatal(err)
	}
	defer func() { _ = f.Close() }()

	client := &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{DisableCompression: true, ForceAttemptHTTP2: true},
	}

	doc := har{Log: harLog{Version: "1.2", Creator: harCreator{Name: "midopress record-corpus", Version: "1"}}}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		u := strings.TrimSpace(sc.Text())
		if u == "" || strings.HasPrefix(u, "#") {
			continue
		}
		e, err := record(client, u, *maxSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", u, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "ok   %-90s %d %-28s %7d bytes enc=%q\n", u, e.Response.Status, e.Response.Content.MimeType, e.Response.Content.Size, header(e.Response.Headers, "Content-Encoding"))
		doc.Log.Entries = append(doc.Log.Entries, e)
	}

	data, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "wrote %d entries to %s\n", len(doc.Log.Entries), *out)
}

func record(c *http.Client, u string, maxSize int) (entry, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return entry{}, err
	}
	for k, v := range browserHeaders {
		req.Header.Set(k, v)
	}
	started := time.Now().UTC()
	resp, err := c.Do(req)
	if err != nil {
		return entry{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return entry{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxSize)+1))
	if err != nil {
		return entry{}, err
	}
	if len(body) > maxSize {
		return entry{}, fmt.Errorf("larger than %d bytes", maxSize)
	}

	e := entry{
		Started: started.Format(time.RFC3339),
		Request: request{Method: http.MethodGet, URL: u},
		Response: response{
			Status:      resp.StatusCode,
			StatusText:  http.StatusText(resp.StatusCode),
			HTTPVersion: resp.Proto,
			Content: content{
				Size:     len(body),
				MimeType: resp.Header.Get("Content-Type"),
				Text:     base64.StdEncoding.EncodeToString(body),
				Encoding: "base64",
			},
		},
		Comment: "the body is stored as received, still encoded as the Content-Encoding header says",
	}
	for k, v := range browserHeaders {
		e.Request.Headers = append(e.Request.Headers, nameValue{k, v})
	}
	for k, vals := range resp.Header {
		for _, v := range vals {
			e.Response.Headers = append(e.Response.Headers, nameValue{k, v})
		}
	}
	return e, nil
}

func header(h []nameValue, name string) string {
	for _, nv := range h {
		if strings.EqualFold(nv.Name, name) {
			return nv.Value
		}
	}
	return ""
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "record-corpus:", err)
	os.Exit(1)
}
