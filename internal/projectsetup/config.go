package projectsetup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/ontopix/engram/internal/fileidentity"
	"github.com/ontopix/engram/internal/harness"
	"github.com/ontopix/engram/internal/lockidentity"
	"github.com/ontopix/engram/internal/transport"
	"go.yaml.in/yaml/v3"
)

var ErrConfigBusy = errors.New("project setup manifest is busy")

var ErrConfigArgument = errors.New("invalid project configuration argument")

type ConfigEffect struct {
	Durable          bool
	RecoveryRequired bool
}

type configPublication struct {
	visible bool
	durable bool
}

type ConfigEffectError struct {
	Effect ConfigEffect
	Err    error
}

func (err *ConfigEffectError) Error() string {
	if err == nil || err.Err == nil {
		return "project setup manifest update failed after mutation"
	}
	return err.Err.Error()
}

func (err *ConfigEffectError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

func ConfigEffectOf(err error) (ConfigEffect, bool) {
	var effect *ConfigEffectError
	if !errors.As(err, &effect) || effect == nil {
		return ConfigEffect{}, false
	}
	return effect.Effect, true
}

type ConfigResult struct {
	Project    string `json:"project"`
	ConfigFile string `json:"config_file"`
	Changed    bool   `json:"changed"`
	Config     Config `json:"config"`
}

type configDocument struct {
	path   string
	raw    []byte
	info   os.FileInfo
	exists bool
	root   yaml.Node
	config Config
}

type configEditor struct {
	beforePublish func(string) error
	afterRename   func(string) error
	afterSync     func(string) error
}

func ShowConfig(project string) (ConfigResult, error) {
	project, err := realDirectory(project)
	if err != nil {
		return ConfigResult{}, err
	}
	document, err := readConfigDocument(project)
	if err != nil {
		return ConfigResult{}, err
	}
	if !document.exists {
		document.root = newConfigNode()
		document.config = Config{Version: 1, Attachments: []ConfigAttachment{}}
		document.raw, err = encodeConfigNode(&document.root)
		if err != nil {
			return ConfigResult{}, err
		}
	}
	return configResult(project, document, false), nil
}

func AddConfigAttachment(project, name, location string) (ConfigResult, error) {
	return (configEditor{}).addAttachment(project, name, location)
}

func (editor configEditor) addAttachment(project, name, location string) (ConfigResult, error) {
	if err := validateConfigAttachment(ConfigAttachment{Name: name, URL: location}, 0); err != nil {
		return ConfigResult{}, fmt.Errorf("%w: %v", ErrConfigArgument, err)
	}
	return editor.update(project, func(document *configDocument) (bool, error) {
		candidate := ConfigAttachment{Name: name, URL: location}
		for _, existing := range document.config.Attachments {
			switch {
			case existing.Name == name && existing.URL == location:
				return false, nil
			case existing.Name == name:
				return false, fmt.Errorf("%w: attachment %q already uses URL %q", ErrConflict, name, existing.URL)
			case existing.URL == location:
				return false, fmt.Errorf("%w: URL %q already belongs to attachment %q", ErrConflict, location, existing.Name)
			}
		}
		document.config.Attachments = append(document.config.Attachments, candidate)
		sort.Slice(document.config.Attachments, func(left, right int) bool {
			return document.config.Attachments[left].Name < document.config.Attachments[right].Name
		})
		return true, addAttachmentNode(&document.root, candidate)
	})
}

func RemoveConfigAttachment(project, name string) (ConfigResult, error) {
	return (configEditor{}).removeAttachment(project, name)
}

func (editor configEditor) removeAttachment(project, name string) (ConfigResult, error) {
	if !validName(name) {
		return ConfigResult{}, fmt.Errorf("%w: attachment name must be a portable lowercase slug", ErrConfigArgument)
	}
	return editor.update(project, func(document *configDocument) (bool, error) {
		index := -1
		for current, attachment := range document.config.Attachments {
			if attachment.Name == name {
				index = current
				break
			}
		}
		if index < 0 {
			return false, nil
		}
		document.config.Attachments = append(document.config.Attachments[:index], document.config.Attachments[index+1:]...)
		return true, removeAttachmentNode(&document.root, name)
	})
}

func SetConfigHarness(project, name string) (ConfigResult, error) {
	return (configEditor{}).setHarness(project, name)
}

func (editor configEditor) setHarness(project, name string) (ConfigResult, error) {
	if _, err := harness.Resolve(name); err != nil {
		return ConfigResult{}, err
	}
	return editor.update(project, func(document *configDocument) (bool, error) {
		if document.config.Harness == name {
			return false, nil
		}
		document.config.Harness = name
		return true, setScalarField(&document.root, "harness", name)
	})
}

func (editor configEditor) update(project string, mutate func(*configDocument) (bool, error)) (result ConfigResult, resultErr error) {
	project, err := realDirectory(project)
	if err != nil {
		return ConfigResult{}, err
	}
	lock, err := acquireConfigLock(filepath.Join(project, ConfigFileName+".lock"))
	if err != nil {
		return ConfigResult{}, err
	}
	published := configPublication{}
	defer func() {
		residual, releaseErr := lock.release()
		if releaseErr != nil {
			resultErr = errors.Join(resultErr, releaseErr)
		}
		existingEffect, hasEffect := ConfigEffectOf(resultErr)
		if resultErr != nil && (hasEffect || published.visible || residual) {
			resultErr = &ConfigEffectError{
				Effect: ConfigEffect{
					Durable:          existingEffect.Durable || published.durable,
					RecoveryRequired: existingEffect.RecoveryRequired || residual,
				},
				Err: resultErr,
			}
		}
		if resultErr != nil {
			result = ConfigResult{}
		}
	}()

	document, err := readConfigDocument(project)
	if err != nil {
		return ConfigResult{}, err
	}
	if !document.exists {
		document.root = newConfigNode()
		document.config = Config{Version: 1, Attachments: []ConfigAttachment{}}
	}
	changed, err := mutate(&document)
	if err != nil {
		return ConfigResult{}, err
	}
	if !changed {
		return configResult(project, document, false), nil
	}
	updated, err := encodeConfigNode(&document.root)
	if err != nil {
		return ConfigResult{}, err
	}
	if _, err := decodeConfig(updated); err != nil {
		return ConfigResult{}, fmt.Errorf("%w: generated manifest is invalid: %v", ErrConfig, err)
	}
	published, err = editor.publish(document, updated)
	if err != nil {
		return ConfigResult{}, err
	}
	document.raw = updated
	document.exists = true
	return configResult(project, document, true), nil
}

func configResult(project string, document configDocument, changed bool) ConfigResult {
	config := document.config
	config.Attachments = append([]ConfigAttachment(nil), config.Attachments...)
	if config.Attachments == nil {
		config.Attachments = []ConfigAttachment{}
	}
	return ConfigResult{
		Project: project, ConfigFile: document.path, Changed: changed,
		Config: config,
	}
}

func readConfigDocument(project string) (configDocument, error) {
	document := configDocument{path: filepath.Join(project, ConfigFileName)}
	info, err := os.Lstat(document.path)
	if errors.Is(err, os.ErrNotExist) {
		return document, nil
	}
	if err != nil {
		return document, fmt.Errorf("%w: inspect %s: %v", ErrConfig, ConfigFileName, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return document, fmt.Errorf("%w: %s is not a real regular file", ErrConfig, ConfigFileName)
	}
	if err := fileidentity.Pin(info); err != nil {
		return document, fmt.Errorf("%w: capture %s identity: %v", ErrConfig, ConfigFileName, err)
	}
	if info.Size() > maxConfigBytes {
		return document, fmt.Errorf("%w: %s exceeds %d bytes", ErrConfig, ConfigFileName, maxConfigBytes)
	}
	file, err := os.Open(document.path)
	if err != nil {
		return document, fmt.Errorf("%w: open %s: %v", ErrConfig, ConfigFileName, err)
	}
	openedBefore, statBeforeErr := file.Stat()
	raw, readErr := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	openedAfter, statAfterErr := file.Stat()
	closeErr := file.Close()
	if err := errors.Join(statBeforeErr, readErr, statAfterErr, closeErr); err != nil {
		return document, fmt.Errorf("%w: read %s: %v", ErrConfig, ConfigFileName, err)
	}
	if len(raw) > maxConfigBytes {
		return document, fmt.Errorf("%w: %s exceeds %d bytes", ErrConfig, ConfigFileName, maxConfigBytes)
	}
	after, err := os.Lstat(document.path)
	if err != nil || openedBefore == nil || openedAfter == nil || after.Mode()&os.ModeSymlink != 0 ||
		!openedBefore.Mode().IsRegular() || !openedAfter.Mode().IsRegular() || !after.Mode().IsRegular() ||
		!sameConfigFile(info, openedBefore) || !sameConfigFile(openedBefore, openedAfter) || !sameConfigFile(openedAfter, after) {
		return document, fmt.Errorf("%w: %s changed while being read", ErrConfig, ConfigFileName)
	}
	config, err := decodeConfig(raw)
	if err != nil {
		return document, fmt.Errorf("%w: %v", ErrConfig, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return document, fmt.Errorf("%w: decode editable YAML: %v", ErrConfig, err)
	}
	document.raw, document.info, document.exists = raw, info, true
	document.root, document.config = root, config
	return document, nil
}

func sameConfigFile(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) && left.Mode() == right.Mode() &&
		left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func validateConfigAttachment(attachment ConfigAttachment, index int) error {
	if !validName(attachment.Name) {
		return fmt.Errorf("attachments[%d].name must be a portable lowercase slug", index)
	}
	if err := transport.ValidateLocation(attachment.URL); err != nil {
		return fmt.Errorf("attachments[%d].url: %v", index, err)
	}
	if hasEmbeddedPassword(attachment.URL) {
		return fmt.Errorf("attachments[%d].url must not contain an embedded password", index)
	}
	return nil
}

func newConfigNode() yaml.Node {
	return yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{
		Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "version"},
			{Kind: yaml.ScalarNode, Tag: "!!int", Value: "1"},
		},
	}}}
}

func setScalarField(document *yaml.Node, field, value string) error {
	root, err := configRoot(document)
	if err != nil {
		return err
	}
	for index := 0; index < len(root.Content); index += 2 {
		if root.Content[index].Value == field {
			target := root.Content[index+1]
			target.Kind, target.Tag, target.Value = yaml.ScalarNode, "!!str", value
			target.Content = nil
			return nil
		}
	}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: field},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
	return nil
}

func addAttachmentNode(document *yaml.Node, attachment ConfigAttachment) error {
	sequence, err := attachmentSequence(document, true)
	if err != nil {
		return err
	}
	sequence.Content = append(sequence.Content, configAttachmentNode(attachment))
	sort.SliceStable(sequence.Content, func(left, right int) bool {
		return attachmentNodeName(sequence.Content[left]) < attachmentNodeName(sequence.Content[right])
	})
	return nil
}

func removeAttachmentNode(document *yaml.Node, name string) error {
	sequence, err := attachmentSequence(document, false)
	if err != nil {
		return err
	}
	for index, attachment := range sequence.Content {
		if attachmentNodeName(attachment) != name {
			continue
		}
		sequence.Content = append(sequence.Content[:index], sequence.Content[index+1:]...)
		return nil
	}
	return fmt.Errorf("%w: attachment %q is missing from editable YAML", ErrConfig, name)
}

func attachmentSequence(document *yaml.Node, create bool) (*yaml.Node, error) {
	root, err := configRoot(document)
	if err != nil {
		return nil, err
	}
	for index := 0; index < len(root.Content); index += 2 {
		if root.Content[index].Value == "attachments" {
			return root.Content[index+1], nil
		}
	}
	if !create {
		return nil, fmt.Errorf("%w: attachments is missing from editable YAML", ErrConfig)
	}
	sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "attachments"}, sequence,
	)
	return sequence, nil
}

func configAttachmentNode(attachment ConfigAttachment) *yaml.Node {
	return &yaml.Node{
		Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "name"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: attachment.Name},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "url"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: attachment.URL},
		},
	}
}

func attachmentNodeName(attachment *yaml.Node) string {
	if attachment == nil || attachment.Kind != yaml.MappingNode {
		return ""
	}
	for index := 0; index+1 < len(attachment.Content); index += 2 {
		if attachment.Content[index].Value == "name" {
			return attachment.Content[index+1].Value
		}
	}
	return ""
}

func configRoot(document *yaml.Node) (*yaml.Node, error) {
	if document == nil || document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: manifest root is not a mapping", ErrConfig)
	}
	return document.Content[0], nil
}

func encodeConfigNode(document *yaml.Node) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("encode %s: %w", ConfigFileName, err)
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (editor configEditor) publish(document configDocument, updated []byte) (configPublication, error) {
	current, err := readConfigDocument(filepath.Dir(document.path))
	if err != nil {
		return configPublication{}, err
	}
	if current.exists != document.exists || !bytes.Equal(current.raw, document.raw) || document.exists && !os.SameFile(document.info, current.info) {
		return configPublication{}, fmt.Errorf("%w: %s changed concurrently", ErrConfigBusy, ConfigFileName)
	}
	mode := os.FileMode(0o644)
	if document.info != nil {
		mode = document.info.Mode().Perm()
	}
	temporary, err := os.CreateTemp(filepath.Dir(document.path), ".engram-config-*")
	if err != nil {
		return configPublication{}, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return configPublication{}, err
	}
	if _, err := temporary.Write(updated); err != nil {
		temporary.Close()
		return configPublication{}, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return configPublication{}, err
	}
	if err := temporary.Close(); err != nil {
		return configPublication{}, err
	}
	if editor.beforePublish != nil {
		if err := editor.beforePublish(document.path); err != nil {
			return configPublication{}, err
		}
	}
	current, err = readConfigDocument(filepath.Dir(document.path))
	if err != nil {
		return configPublication{}, err
	}
	if current.exists != document.exists || !bytes.Equal(current.raw, document.raw) || document.exists && !os.SameFile(document.info, current.info) {
		return configPublication{}, fmt.Errorf("%w: %s changed concurrently", ErrConfigBusy, ConfigFileName)
	}
	if err := os.Rename(temporaryName, document.path); err != nil {
		return configPublication{}, err
	}
	published := configPublication{visible: true}
	if editor.afterRename != nil {
		if err := editor.afterRename(document.path); err != nil {
			return published, err
		}
	}
	if err := syncConfigDirectory(filepath.Dir(document.path)); err != nil {
		return published, err
	}
	published.durable = true
	if editor.afterSync != nil {
		if err := editor.afterSync(document.path); err != nil {
			return published, err
		}
	}
	return published, nil
}

type configLock struct {
	path     string
	file     *os.File
	identity lockidentity.Identity
}

func acquireConfigLock(path string) (*configLock, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, ErrConfigBusy
	}
	if err != nil {
		return nil, err
	}
	identity, err := lockidentity.Establish(file)
	if err != nil {
		closeErr := file.Close()
		return nil, &ConfigEffectError{
			Effect: ConfigEffect{RecoveryRequired: true},
			Err:    errors.Join(fmt.Errorf("establish configuration lock identity: %w", err), closeErr),
		}
	}
	return &configLock{path: path, file: file, identity: identity}, nil
}

func (lock *configLock) release() (bool, error) {
	if lock == nil {
		return false, nil
	}
	_, statErr := lock.file.Stat()
	closeErr := lock.file.Close()
	state, inspectErr := lock.identity.Inspect(lock.path)
	if state == lockidentity.Other && inspectErr == nil {
		inspectErr = ErrConfigBusy
	}
	var removeErr error
	if statErr == nil && inspectErr == nil && state == lockidentity.Owned {
		removeErr = os.Remove(lock.path)
	}
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	var syncErr error
	if state == lockidentity.Owned && removeErr == nil {
		syncErr = syncConfigDirectory(filepath.Dir(lock.path))
	}
	residual := false
	remaining, remainingErr := lock.identity.Inspect(lock.path)
	if remaining == lockidentity.Owned {
		residual = true
	}
	if remaining == lockidentity.Other && remainingErr != nil {
		residual = true
	}
	return residual, errors.Join(statErr, closeErr, inspectErr, removeErr, syncErr, remainingErr)
}

func syncConfigDirectory(directory string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(handle.Sync(), handle.Close())
}
