// Package kubeconfig merges kubemg's entries into a kubeconfig file without
// disturbing anything else in it.
//
// It works on the YAML node tree rather than on a decoded struct, which is the
// whole point: a kubeconfig is a file people annotate and other tools write
// to, and decoding it into a struct and encoding it back would drop every
// comment and every field the struct did not know about. Here an entry kubemg
// owns is replaced in place and everything else is left exactly as it was.
//
// There is no client-go: it would be most of the binary, for a file format
// that is three lists and a string.
package kubeconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/kubemg/kubemg/cli/internal/store"
)

// ExecAPIVersion is the credential plugin API every supported kubectl speaks.
const ExecAPIVersion = "client.authentication.k8s.io/v1"

// TargetPath is the file to write, kubectl's way: an explicit path, else the
// first entry of $KUBECONFIG, else ~/.kube/config.
func TargetPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv("KUBECONFIG"); env != "" {
		for _, path := range filepath.SplitList(env) {
			if path != "" {
				return path, nil
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the home directory: %w", err)
	}
	return filepath.Join(home, ".kube", "config"), nil
}

// File is a kubeconfig being edited.
type File struct {
	path    string
	existed bool
	doc     *yaml.Node
}

// Load reads a kubeconfig, or starts an empty one if the file does not exist.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &File{path: path, doc: emptyDocument()}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	doc, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return &File{path: path, existed: true, doc: doc}, nil
}

func parse(data []byte) (*yaml.Node, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return emptyDocument(), nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("not a kubeconfig: the top level is not a mapping")
	}
	return &doc, nil
}

func emptyDocument() *yaml.Node {
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setKey(root, "apiVersion", scalar("v1"))
	setKey(root, "kind", scalar("Config"))
	setKey(root, "preferences", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
	setKey(root, "clusters", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
	setKey(root, "users", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
	setKey(root, "contexts", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
	setKey(root, "current-context", scalar(""))
	return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
}

func (f *File) root() *yaml.Node { return f.doc.Content[0] }

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func boolean(value bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(value)}
}

func getKey(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func setKey(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			// The value is replaced, the key node kept: a comment on the key
			// stays where its author put it.
			value.HeadComment = mapping.Content[i+1].HeadComment
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, scalar(key), value)
}

// list returns the named top-level list, making one where the file has none —
// or has `null`, which is how kubectl writes an empty one.
func (f *File) list(key string) *yaml.Node {
	node := getKey(f.root(), key)
	if node == nil || node.Kind != yaml.SequenceNode {
		node = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		setKey(f.root(), key, node)
	}
	return node
}

// upsert replaces the body of the named entry in a list (`cluster:`, `user:`
// or `context:`), or appends a new entry. The entry's own node — and any
// comment on it — survives a replace.
func (f *File) upsert(listKey, bodyKey, name string, body *yaml.Node) {
	list := f.list(listKey)
	for _, item := range list.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		if n := getKey(item, "name"); n != nil && n.Value == name {
			setKey(item, bodyKey, body)
			return
		}
	}
	item := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setKey(item, "name", scalar(name))
	setKey(item, bodyKey, body)
	list.Content = append(list.Content, item)
}

// SetCluster writes a cluster entry.
func (f *File) SetCluster(name, server, caData string) {
	body := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setKey(body, "server", scalar(server))
	if caData != "" {
		setKey(body, "certificate-authority-data", scalar(caData))
	}
	f.upsert("clusters", "cluster", name, body)
}

// Exec is the credential plugin a user entry runs.
type Exec struct {
	Command     string
	Args        []string
	InstallHint string
}

// SetExecUser writes a user entry that asks kubemg for its credential.
// interactiveMode is Never because the plugin never prompts: an ended session
// is an error that says to run `kubemg login`, not a prompt kubectl would hang
// on inside a script.
func (f *File) SetExecUser(name string, exec Exec) {
	execNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setKey(execNode, "apiVersion", scalar(ExecAPIVersion))
	setKey(execNode, "command", scalar(exec.Command))
	args := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, arg := range exec.Args {
		args.Content = append(args.Content, scalar(arg))
	}
	setKey(execNode, "args", args)
	setKey(execNode, "interactiveMode", scalar("Never"))
	setKey(execNode, "provideClusterInfo", boolean(false))
	if exec.InstallHint != "" {
		setKey(execNode, "installHint", scalar(exec.InstallHint))
	}
	body := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setKey(body, "exec", execNode)
	f.upsert("users", "user", name, body)
}

// SetContext writes a context entry.
func (f *File) SetContext(name, cluster, user, namespace string) {
	body := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setKey(body, "cluster", scalar(cluster))
	setKey(body, "user", scalar(user))
	if namespace != "" {
		setKey(body, "namespace", scalar(namespace))
	}
	f.upsert("contexts", "context", name, body)
}

// SetCurrentContext makes a context the one kubectl uses.
func (f *File) SetCurrentContext(name string) {
	setKey(f.root(), "current-context", scalar(name))
}

// TokenFrom reads the bearer token out of the static kubeconfig the server
// rendered, and nothing else.
//
// The rendered file is never merged as it is: a user entry can carry an
// `exec` command or an auth provider, and copying one from a server into a
// laptop's kubeconfig would let a compromised or impostor server run a program
// there the next time kubectl used the context. So the one value this needs is
// taken out, it must be a plain token, and every entry is written by
// SetCluster/SetTokenUser/SetContext from values this CLI chose.
func TokenFrom(rendered []byte) (string, error) {
	doc, err := parse(rendered)
	if err != nil {
		return "", fmt.Errorf("the server's kubeconfig: %w", err)
	}
	users := getKey(doc.Content[0], "users")
	if users == nil || users.Kind != yaml.SequenceNode || len(users.Content) != 1 {
		return "", errors.New("the server's kubeconfig does not hold exactly one user")
	}
	user := getKey(users.Content[0], "user")
	if user == nil || user.Kind != yaml.MappingNode {
		return "", errors.New("the server's kubeconfig user has no credential")
	}
	for i := 0; i+1 < len(user.Content); i += 2 {
		if key := user.Content[i].Value; key != "token" {
			return "", fmt.Errorf("the server's kubeconfig user carries %q — only a token is accepted", key)
		}
	}
	token := getKey(user, "token")
	if token == nil || token.Kind != yaml.ScalarNode || strings.TrimSpace(token.Value) == "" {
		return "", errors.New("the server's kubeconfig user has no token")
	}
	return token.Value, nil
}

// SetTokenUser writes a user entry that carries a bearer token.
func (f *File) SetTokenUser(name, token string) {
	body := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setKey(body, "token", scalar(token))
	f.upsert("users", "user", name, body)
}

// Save writes the file: the previous version kept beside it as `.bak`, the new
// one written whole through a rename and readable only by its owner — it names
// servers, and a static one carries a token.
func (f *File) Save() error {
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(f.doc); err != nil {
		return fmt.Errorf("render %s: %w", f.path, err)
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	if f.existed {
		if previous, err := os.ReadFile(f.path); err == nil {
			if err := store.WriteFileAtomic(f.path+".bak", previous); err != nil {
				return fmt.Errorf("keep a backup of %s: %w", f.path, err)
			}
		}
	}
	return store.WriteFileAtomic(f.path, buf.Bytes())
}

// Path is the file being edited.
func (f *File) Path() string { return f.path }

// Quote renders a value for a shell line in a message.
func Quote(value string) string {
	if strings.ContainsAny(value, " \t'\"$`\\") {
		return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
	}
	return value
}
