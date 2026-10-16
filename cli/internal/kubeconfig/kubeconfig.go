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

// MarkerName is the extension kubemg writes into every entry it owns. It is
// what lets a later run tell its own entries from everybody else's.
const MarkerName = "kubemg"

// ForeignEntryError is an entry kubemg was asked to write over that it did not
// write. It is refused, not replaced: an entry another tool or person made —
// a production cluster, an admin user — is exactly what a lying server would
// most like to point somewhere else, and replacing its address would send that
// entry's own credentials there.
type ForeignEntryError struct {
	Kind, Name string
}

func (e *ForeignEntryError) Error() string {
	return fmt.Sprintf("there is already a %s named %q that kubemg did not write; it was left alone — remove it, or choose another name", e.Kind, e.Name)
}

// marker is the ownership extension, naming the server the entry belongs to.
func marker(owner string) *yaml.Node {
	ext := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setKey(ext, "server", scalar(owner))
	item := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setKey(item, "name", scalar(MarkerName))
	setKey(item, "extension", ext)
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{item}}
}

// ownedBy reports whether an entry's body carries kubemg's marker for owner.
func ownedBy(body *yaml.Node, owner string) bool {
	if body == nil || body.Kind != yaml.MappingNode {
		return false
	}
	extensions := getKey(body, "extensions")
	if extensions == nil || extensions.Kind != yaml.SequenceNode {
		return false
	}
	for _, item := range extensions.Content {
		if name := getKey(item, "name"); name == nil || name.Value != MarkerName {
			continue
		}
		if ext := getKey(item, "extension"); ext != nil {
			if server := getKey(ext, "server"); server != nil && server.Value == owner {
				return true
			}
		}
	}
	return false
}

// upsert writes the body of the named entry in a list (`cluster:`, `user:` or
// `context:`), marked as owner's. An existing entry is replaced only if this
// owner wrote it; anything else is refused with a ForeignEntryError. The
// entry's own node — and any comment on it — survives a replace.
func (f *File) upsert(listKey, bodyKey, name string, body *yaml.Node, owner string) error {
	setKey(body, "extensions", marker(owner))
	list := f.list(listKey)
	for _, item := range list.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		if n := getKey(item, "name"); n != nil && n.Value == name {
			if !ownedBy(getKey(item, bodyKey), owner) {
				return &ForeignEntryError{Kind: bodyKey, Name: name}
			}
			setKey(item, bodyKey, body)
			return nil
		}
	}
	item := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setKey(item, "name", scalar(name))
	setKey(item, bodyKey, body)
	list.Content = append(list.Content, item)
	return nil
}

// SetCluster writes a cluster entry owned by owner.
func (f *File) SetCluster(owner, name, server, caData string) error {
	body := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setKey(body, "server", scalar(server))
	if caData != "" {
		setKey(body, "certificate-authority-data", scalar(caData))
	}
	return f.upsert("clusters", "cluster", name, body, owner)
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
func (f *File) SetExecUser(owner, name string, exec Exec) error {
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
	return f.upsert("users", "user", name, body, owner)
}

// SetContext writes a context entry owned by owner.
func (f *File) SetContext(owner, name, cluster, user, namespace string) error {
	body := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setKey(body, "cluster", scalar(cluster))
	setKey(body, "user", scalar(user))
	if namespace != "" {
		setKey(body, "namespace", scalar(namespace))
	}
	return f.upsert("contexts", "context", name, body, owner)
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

// SetTokenUser writes a user entry, owned by owner, that carries a token.
func (f *File) SetTokenUser(owner, name, token string) error {
	body := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setKey(body, "token", scalar(token))
	return f.upsert("users", "user", name, body, owner)
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
