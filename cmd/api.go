package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ysmaoui/jkit/internal/api"
	"github.com/ysmaoui/jkit/internal/config"
	"github.com/ysmaoui/jkit/internal/jenkins"
	"github.com/ysmaoui/jkit/internal/output"
)

var apiCmd = &cobra.Command{
	Use:   "api <path|url>",
	Short: "Send an authenticated request to the Jenkins API and print the response",
	Long: `Send an authenticated request to Jenkins and print the response body.

PATH is relative to the host root (leading slash optional) and goes to --host,
an alias or the default host. A full http(s) URL picks the configured host it
belongs to; with --host as well, the URL must lie under that host. The query
string is sent exactly as written, so tree=jobs[name] needs no escaping.

A JSON body is pretty-printed when stdout is a terminal or with --json, and
printed as received otherwise. --format applies a Go template to the decoded
JSON body. On HTTP 400 and above the body is still printed and the command
exits non-zero.

-d and --data-urlencode send a form body (application/x-www-form-urlencoded
unless -H sets Content-Type), joined with & in the order given. --input sends
a file, or stdin with -, as the body; set its Content-Type with -H. A body
makes POST the default method.`,
	Example: `  jkit api /api/json
  jkit api '/computer/api/json?tree=computer[displayName,offline]'
  jkit api https://jenkins.example.com/job/my-app/lastBuild/api/json
  jkit api /api/json --include
  jkit api /job/my-app/api/json --format '{{.color}}'
  jkit api -X POST /job/my-app/build
  jkit api /manage/log/newLogRecorder --data-urlencode name=my-recorder
  jkit api /job/my-app/config.xml --input config.xml -H 'Content-Type: application/xml'`,
	Args: cobra.ExactArgs(1),
	RunE: runAPI,
}

func init() {
	registerAPIFlags(apiCmd)
	rootCmd.AddCommand(apiCmd)
}

func registerAPIFlags(c *cobra.Command) {
	c.Flags().StringP("method", "X", "GET", "HTTP method; methods other than GET, HEAD and OPTIONS send a CSRF crumb")
	c.Flags().StringArrayP("header", "H", nil, `Request header "Key: Value" (repeatable)`)
	c.Flags().BoolP("include", "i", false, "Print the status line and response headers before the body")
	var fields []string
	c.Flags().VarP(&formFlag{fields: &fields}, "data", "d", "Form field sent as given, e.g. name=value (repeatable)")
	c.Flags().Var(&formFlag{fields: &fields, encode: true}, "data-urlencode", "Form field name=value with value URL-encoded (repeatable)")
	c.Flags().String("input", "", "File to send as the request body, - for stdin")
}

// formFlag collects -d and --data-urlencode into one shared list, so the body
// keeps the order the fields were given in across both flags, as curl does.
type formFlag struct {
	fields *[]string
	encode bool
}

func (f *formFlag) String() string { return strings.Join(*f.fields, "&") }
func (f *formFlag) Type() string   { return "string" }

func (f *formFlag) Set(v string) error {
	if f.encode {
		if name, value, ok := strings.Cut(v, "="); ok {
			v = name + "=" + url.QueryEscape(value)
		} else {
			v = url.QueryEscape(v)
		}
	}
	*f.fields = append(*f.fields, v)
	return nil
}

func (f *formFlag) Append(v string) error { return f.Set(v) }
func (f *formFlag) GetSlice() []string    { return *f.fields }

func (f *formFlag) Replace(vs []string) error {
	*f.fields = nil
	for _, v := range vs {
		if err := f.Set(v); err != nil {
			return err
		}
	}
	return nil
}

// requestBody returns the body that -d, --data-urlencode or --input ask for,
// nil for none, and sets the form Content-Type unless header has one.
func requestBody(cmd *cobra.Command, header http.Header) ([]byte, error) {
	fields := cmd.Flags().Lookup("data").Value.(*formFlag).GetSlice()
	input, _ := cmd.Flags().GetString("input")
	switch {
	case input != "" && len(fields) > 0:
		return nil, fmt.Errorf("--input cannot be used with -d or --data-urlencode")
	case input == "-":
		body, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return nil, fmt.Errorf("reading stdin: %w", err)
		}
		return body, nil
	case input != "":
		return os.ReadFile(input)
	case len(fields) > 0:
		if header.Get("Content-Type") == "" {
			header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return []byte(strings.Join(fields, "&")), nil
	}
	return nil, nil
}

// stdoutIsTerminal is a variable so tests can take the terminal path.
var stdoutIsTerminal = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

func runAPI(cmd *cobra.Command, args []string) error {
	target := args[0]
	method, _ := cmd.Flags().GetString("method")
	method = strings.ToUpper(method)
	rawHeaders, _ := cmd.Flags().GetStringArray("header")
	include, _ := cmd.Flags().GetBool("include")
	asJSON, _ := cmd.Flags().GetBool("json")
	tmpl, _ := cmd.Flags().GetString("format")

	header, err := parseHeaders(rawHeaders)
	if err != nil {
		return err
	}
	reqBody, err := requestBody(cmd, header)
	if err != nil {
		return err
	}
	if reqBody != nil {
		if !cmd.Flags().Changed("method") {
			method = http.MethodPost
		}
		if method == http.MethodGet || method == http.MethodHead {
			return fmt.Errorf("a request body cannot be sent with %s; use -X POST", method)
		}
	}
	client, err := apiClient(cmd, target)
	if err != nil {
		return err
	}

	resp, err := client.Raw(context.Background(), method, target, header, reqBody)
	if err != nil {
		return err
	}
	defer api.CloseBody(resp)

	w := os.Stdout
	if include {
		writeStatusAndHeaders(w, resp)
	}
	if resp.StatusCode < 400 && !asJSON && tmpl == "" && !stdoutIsTerminal() {
		if _, err := io.Copy(w, resp.Body); err != nil {
			return fmt.Errorf("reading response: %w", err)
		}
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		// The body is the explanation, so it is shown as-is rather than
		// refused for not matching --json or --format.
		writeBody(w, body, stdoutIsTerminal())
		err := api.StatusError(resp, nil)
		var nf *jenkins.NotFoundError
		if errors.As(err, &nf) {
			nf.Hint = "check the path"
		}
		return err
	}

	switch {
	case asJSON:
		if !json.Valid(body) {
			return fmt.Errorf("--json: response body is not JSON (Content-Type %q)", resp.Header.Get("Content-Type"))
		}
		writeBody(w, body, true)
	case tmpl != "":
		var data any
		if err := json.Unmarshal(body, &data); err != nil {
			return fmt.Errorf("--format: response body is not JSON: %w", err)
		}
		return output.NewFormatter(w, false, tmpl).Output(data, nil)
	default:
		writeBody(w, body, stdoutIsTerminal())
	}
	return nil
}

// apiClient picks the host for target: a path, or a full URL with --host, goes
// to the usual --host, alias or default host, which refuses a URL not under it;
// a full URL alone goes to the configured host that serves it.
func apiClient(cmd *cobra.Command, target string) (*api.Client, error) {
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		client, _, err := clientFromCmd(cmd)
		return client, err
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid URL %q", target)
	}
	if h, _ := cmd.Flags().GetString("host"); h != "" {
		client, _, err := clientFromCmd(cmd)
		return client, err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return clientFromURL(cfg, u.Scheme+"://"+u.Host, clientOpts(cmd)...)
}

func parseHeaders(raw []string) (http.Header, error) {
	h := http.Header{}
	for _, kv := range raw {
		k, v, ok := strings.Cut(kv, ":")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf(`invalid header %q, want "Key: Value"`, kv)
		}
		h.Add(k, strings.TrimSpace(v))
	}
	return h, nil
}

func writeStatusAndHeaders(w io.Writer, resp *http.Response) {
	_, _ = fmt.Fprintf(w, "%s %s\n", resp.Proto, resp.Status)
	keys := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range resp.Header[k] {
			_, _ = fmt.Fprintf(w, "%s: %s\n", k, v)
		}
	}
	_, _ = fmt.Fprintln(w)
}

// writeBody indents body when pretty is set and body is JSON, and writes it
// unchanged otherwise.
func writeBody(w io.Writer, body []byte, pretty bool) {
	if pretty {
		var buf bytes.Buffer
		if json.Indent(&buf, body, "", "  ") == nil {
			buf.WriteByte('\n')
			_, _ = w.Write(buf.Bytes())
			return
		}
	}
	_, _ = w.Write(body)
}
