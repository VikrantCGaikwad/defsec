package kubernetes

import (
	"context"
	"io"
	"io/fs"
	"path/filepath"
	"sync"

	"github.com/aquasecurity/defsec/pkg/types"

	"github.com/aquasecurity/defsec/pkg/framework"

	"github.com/aquasecurity/defsec/pkg/debug"

	"github.com/aquasecurity/defsec/pkg/scanners/options"

	"github.com/liamg/memoryfs"

	"github.com/aquasecurity/defsec/pkg/rego"
	"github.com/aquasecurity/defsec/pkg/scanners/kubernetes/parser"

	"github.com/aquasecurity/defsec/pkg/scan"

	"github.com/aquasecurity/defsec/pkg/scanners"
)

var _ scanners.FSScanner = (*Scanner)(nil)
var _ options.ConfigurableScanner = (*Scanner)(nil)

type Scanner struct {
	debug         debug.Logger
	options       []options.ScannerOption
	policyDirs    []string
	policyReaders []io.Reader
	regoScanner   *rego.Scanner
	parser        *parser.Parser
	skipRequired  bool
	sync.Mutex
	loadEmbedded bool
	frameworks   []framework.Framework
	spec         string
}

func (s *Scanner) SetSpec(spec string) {
	s.spec = spec
}

func (s *Scanner) SetRegoOnly(bool) {}

func (s *Scanner) SetFrameworks(frameworks []framework.Framework) {
	s.frameworks = frameworks
}

func (s *Scanner) SetUseEmbeddedPolicies(b bool) {
	s.loadEmbedded = b
}

func (s *Scanner) SetPolicyReaders(readers []io.Reader) {
	s.policyReaders = readers
}

func (s *Scanner) SetSkipRequiredCheck(skip bool) {
	s.skipRequired = skip
}

func (s *Scanner) SetDebugWriter(writer io.Writer) {
	s.debug = debug.New(writer, "kubernetes", "scanner")
}

func (s *Scanner) SetTraceWriter(_ io.Writer) {
}

func (s *Scanner) SetPerResultTracingEnabled(_ bool) {
}

func (s *Scanner) SetPolicyDirs(dirs ...string) {
	s.policyDirs = dirs
}

func (s *Scanner) SetDataDirs(...string) {}
func (s *Scanner) SetPolicyNamespaces(_ ...string) {
}

func (s *Scanner) SetPolicyFilesystem(_ fs.FS) {
	// handled by rego when option is passed on
}

func (s *Scanner) SetDataFilesystem(_ fs.FS) {
	// handled by rego when option is passed on
}

func NewScanner(opts ...options.ScannerOption) *Scanner {
	s := &Scanner{
		options: opts,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.parser = parser.New(options.ParserWithSkipRequiredCheck(s.skipRequired))
	return s
}

func (s *Scanner) Name() string {
	return "Kubernetes"
}

func (s *Scanner) initRegoScanner(srcFS fs.FS) (*rego.Scanner, error) {
	s.Lock()
	defer s.Unlock()
	if s.regoScanner != nil {
		return s.regoScanner, nil
	}
	regoScanner := rego.NewScanner(types.SourceKubernetes, s.options...)
	regoScanner.SetParentDebugLogger(s.debug)
	if err := regoScanner.LoadPolicies(s.loadEmbedded, srcFS, s.policyDirs, s.policyReaders); err != nil {
		return nil, err
	}
	s.regoScanner = regoScanner
	return regoScanner, nil
}

func (s *Scanner) ScanReader(ctx context.Context, filename string, reader io.Reader) (scan.Results, error) {
	memfs := memoryfs.New()
	if err := memfs.MkdirAll(filepath.Base(filename), 0o700); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if err := memfs.WriteFile(filename, data, 0o644); err != nil {
		return nil, err
	}
	return s.ScanFS(ctx, memfs, ".")
}

func (s *Scanner) ScanFS(ctx context.Context, target fs.FS, dir string) (scan.Results, error) {

	k8sFilesets, err := s.parser.ParseFS(ctx, target, dir)
	if err != nil {
		return nil, err
	}

	if len(k8sFilesets) == 0 {
		return nil, nil
	}

	var inputs []rego.Input
	for path, k8sFiles := range k8sFilesets {
		for _, content := range k8sFiles {
			inputs = append(inputs, rego.Input{
				Path:     path,
				FS:       target,
				Contents: content,
			})
		}
	}

	regoScanner, err := s.initRegoScanner(target)
	if err != nil {
		return nil, err
	}

	s.debug.Log("Scanning %d files...", len(inputs))
	results, err := regoScanner.ScanInput(ctx, inputs...)
	if err != nil {
		return nil, err
	}
	results.SetSourceAndFilesystem("", target, false)
	return results, nil
}
