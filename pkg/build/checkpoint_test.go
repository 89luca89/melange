// Copyright 2024 Chainguard, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package build

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	apko_build "chainguard.dev/apko/pkg/build"
	apko_types "chainguard.dev/apko/pkg/build/types"
	"chainguard.dev/melange/pkg/config"
	"chainguard.dev/melange/pkg/container"
	"github.com/chainguard-dev/clog/slogtest"
)

// mockRunner implements container.Runner for testing checkpoint functionality.
type mockRunner struct {
	workspaceTarData []byte
	workspaceTarErr  error
}

func (m *mockRunner) Close() error                                  { return nil }
func (m *mockRunner) Name() string                                  { return "mock" }
func (m *mockRunner) TestUsability(ctx context.Context) bool        { return true }
func (m *mockRunner) OCIImageLoader() container.Loader              { return nil }
func (m *mockRunner) StartPod(ctx context.Context, cfg *container.Config) error { return nil }
func (m *mockRunner) Run(ctx context.Context, cfg *container.Config, envOverride map[string]string, cmd ...string) error {
	return nil
}
func (m *mockRunner) TerminatePod(ctx context.Context, cfg *container.Config) error { return nil }
func (m *mockRunner) TempDir() string                                               { return "" }
func (m *mockRunner) GetReleaseData(ctx context.Context, cfg *container.Config) (*apko_build.ReleaseData, error) {
	return nil, nil
}

func (m *mockRunner) WorkspaceTar(ctx context.Context, cfg *container.Config, extraFiles []string) (io.ReadCloser, error) {
	if m.workspaceTarErr != nil {
		return nil, m.workspaceTarErr
	}
	if m.workspaceTarData == nil {
		return nil, nil
	}
	return io.NopCloser(bytes.NewReader(m.workspaceTarData)), nil
}

func (m *mockRunner) CheckpointTar(ctx context.Context, cfg *container.Config) (io.ReadCloser, error) {
	if m.workspaceTarErr != nil {
		return nil, m.workspaceTarErr
	}
	if m.workspaceTarData == nil {
		return nil, nil
	}
	return io.NopCloser(bytes.NewReader(m.workspaceTarData)), nil
}

// createTarData creates a tar archive with the given files for testing.
func createTarData(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing tar header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("writing tar content: %v", err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	return buf.Bytes()
}

func TestCheckpointPath(t *testing.T) {
	b := &Build{
		CheckpointsDir: "/tmp/my-checkpoints",
		Arch:           apko_types.Architecture("aarch64"),
		Configuration:  &config.Configuration{Package: config.Package{Name: "zlib"}},
	}

	got := b.checkpointPath("after-configure")
	want := "/tmp/my-checkpoints/checkpoints/aarch64/zlib/after-configure.tar"
	if got != want {
		t.Errorf("checkpointPath() = %q, want %q", got, want)
	}
}

func TestCheckpointPathDefaultDir(t *testing.T) {
	b := &Build{
		CheckpointsDir: "", // empty = use current directory
		Arch:           apko_types.Architecture("x86_64"),
		Configuration:  &config.Configuration{Package: config.Package{Name: "foo"}},
	}

	got := b.checkpointPath("my-checkpoint")
	want := "checkpoints/x86_64/foo/my-checkpoint.tar"
	if got != want {
		t.Errorf("checkpointPath() = %q, want %q", got, want)
	}
}

func TestFindLatestCheckpoint(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]time.Duration // filename -> age (negative = in the past)
		want  string
	}{
		{
			name:  "no checkpoints directory",
			files: nil,
			want:  "",
		},
		{
			name:  "empty directory",
			files: map[string]time.Duration{},
			want:  "",
		},
		{
			name: "single checkpoint",
			files: map[string]time.Duration{
				"after-configure.tar": -1 * time.Hour,
			},
			want: "after-configure",
		},
		{
			name: "multiple checkpoints returns latest",
			files: map[string]time.Duration{
				"old-checkpoint.tar": -2 * time.Hour,
				"new-checkpoint.tar": -1 * time.Minute,
			},
			want: "new-checkpoint",
		},
		{
			name: "ignores non-tar files",
			files: map[string]time.Duration{
				"readme.txt":    -1 * time.Minute,
				"checkpoint.md": -1 * time.Minute,
			},
			want: "",
		},
		{
			name: "ignores directories",
			files: map[string]time.Duration{
				"subdir.tar/": -1 * time.Minute, // trailing slash indicates directory
			},
			want: "",
		},
		{
			name: "mixed files returns latest tar",
			files: map[string]time.Duration{
				"readme.txt":          -1 * time.Minute,
				"old-checkpoint.tar":  -2 * time.Hour,
				"new-checkpoint.tar":  -30 * time.Minute,
				"newest-but-not-tar":  -1 * time.Second,
			},
			want: "new-checkpoint",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := slogtest.Context(t)
			tmpDir := t.TempDir()

			b := &Build{
				CheckpointsDir: tmpDir,
				Arch:           apko_types.Architecture("x86_64"),
				Configuration:  &config.Configuration{Package: config.Package{Name: "test-pkg"}},
			}

			if tc.files != nil {
				// Create the checkpoint directory structure
				checkpointDir := filepath.Join(tmpDir, "checkpoints", "x86_64", "test-pkg")
				if err := os.MkdirAll(checkpointDir, 0755); err != nil {
					t.Fatalf("creating checkpoint dir: %v", err)
				}

				now := time.Now()
				for name, age := range tc.files {
					if name[len(name)-1] == '/' {
						// Create directory
						if err := os.MkdirAll(filepath.Join(checkpointDir, name[:len(name)-1]), 0755); err != nil {
							t.Fatalf("creating subdir: %v", err)
						}
						continue
					}
					path := filepath.Join(checkpointDir, name)
					if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
						t.Fatalf("writing file: %v", err)
					}
					modTime := now.Add(age)
					if err := os.Chtimes(path, modTime, modTime); err != nil {
						t.Fatalf("setting mod time: %v", err)
					}
				}
			}

			got := b.findLatestCheckpoint(ctx)
			if got != tc.want {
				t.Errorf("findLatestCheckpoint() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCaptureCheckpoint(t *testing.T) {
	ctx := slogtest.Context(t)
	tmpDir := t.TempDir()

	// Create mock tar data
	tarData := createTarData(t, map[string]string{
		"file1.txt": "content1",
		"file2.txt": "content2",
	})

	checkpointPath := func(name string) string {
		return filepath.Join(tmpDir, "checkpoints", "x86_64", "test-pkg", name+".tar")
	}

	pr := &pipelineRunner{
		runner:         &mockRunner{workspaceTarData: tarData},
		config:         &container.Config{},
		checkpointPath: checkpointPath,
	}

	// Capture the checkpoint
	ran, err := pr.captureCheckpoint(ctx, "my-checkpoint")
	if err != nil {
		t.Fatalf("captureCheckpoint() error: %v", err)
	}
	if !ran {
		t.Errorf("captureCheckpoint() returned ran=false, want true")
	}

	// Verify checkpoint file was created
	expectedPath := checkpointPath("my-checkpoint")
	gotData, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("reading checkpoint file: %v", err)
	}

	if !bytes.Equal(gotData, tarData) {
		t.Errorf("checkpoint file content mismatch: got %d bytes, want %d bytes", len(gotData), len(tarData))
	}
}

func TestCaptureCheckpointNilTar(t *testing.T) {
	ctx := slogtest.Context(t)
	tmpDir := t.TempDir()

	checkpointPath := func(name string) string {
		return filepath.Join(tmpDir, "checkpoints", "x86_64", "test-pkg", name+".tar")
	}

	pr := &pipelineRunner{
		runner:         &mockRunner{workspaceTarData: nil}, // Returns nil
		config:         &container.Config{},
		checkpointPath: checkpointPath,
	}

	// Should succeed but not create a tar file (directory may be created)
	ran, err := pr.captureCheckpoint(ctx, "nil-checkpoint")
	if err != nil {
		t.Fatalf("captureCheckpoint() error: %v", err)
	}
	if !ran {
		t.Errorf("captureCheckpoint() returned ran=false, want true")
	}

	// Verify no checkpoint tar file was created
	expectedPath := checkpointPath("nil-checkpoint")
	if _, err := os.Stat(expectedPath); !os.IsNotExist(err) {
		t.Errorf("checkpoint file should not exist, but got: %v", err)
	}
}

func TestCaptureCheckpointError(t *testing.T) {
	ctx := slogtest.Context(t)
	tmpDir := t.TempDir()

	checkpointPath := func(name string) string {
		return filepath.Join(tmpDir, "checkpoints", "x86_64", "test-pkg", name+".tar")
	}

	pr := &pipelineRunner{
		runner:         &mockRunner{workspaceTarErr: io.ErrUnexpectedEOF},
		config:         &container.Config{},
		checkpointPath: checkpointPath,
	}

	_, err := pr.captureCheckpoint(ctx, "error-checkpoint")
	if err == nil {
		t.Fatal("captureCheckpoint() should return error when runner fails")
	}
}

func TestRunPipelineCheckpointDisabled(t *testing.T) {
	ctx := slogtest.Context(t)

	pr := &pipelineRunner{
		runner:         &mockRunner{},
		config:         &container.Config{},
		checkpointPath: nil, // checkpoints disabled
	}

	// A checkpoint pipeline should be skipped when checkpoints are disabled
	pipeline := &config.Pipeline{
		Checkpoint: "my-checkpoint",
	}

	ran, err := pr.runPipeline(ctx, pipeline)
	if err != nil {
		t.Fatalf("runPipeline() error: %v", err)
	}
	if !ran {
		t.Errorf("runPipeline() returned ran=false, want true")
	}
	// No error, no crash - checkpoint was silently skipped
}

func TestPipelineRunnerSkipping(t *testing.T) {
	ctx := slogtest.Context(t)

	t.Run("skips non-checkpoint pipelines when skipping", func(t *testing.T) {
		pr := &pipelineRunner{
			runner:              &mockRunner{},
			config:              &container.Config{},
			skipUntilCheckpoint: "target-checkpoint",
			skipping:            true,
		}

		// Run a regular pipeline - should be skipped
		pipeline := &config.Pipeline{
			Runs: "echo hello",
		}

		ran, err := pr.runPipeline(ctx, pipeline)
		if err != nil {
			t.Fatalf("runPipeline() error: %v", err)
		}
		if !ran {
			t.Errorf("runPipeline() returned ran=false, want true (skipped pipelines return true)")
		}
		if !pr.skipping {
			t.Errorf("skipping should still be true after non-matching pipeline")
		}
	})

	t.Run("stops skipping at matching checkpoint", func(t *testing.T) {
		pr := &pipelineRunner{
			runner:              &mockRunner{},
			config:              &container.Config{},
			skipUntilCheckpoint: "target-checkpoint",
			skipping:            true,
		}

		// Run the target checkpoint pipeline
		pipeline := &config.Pipeline{
			Checkpoint: "target-checkpoint",
		}

		ran, err := pr.runPipeline(ctx, pipeline)
		if err != nil {
			t.Fatalf("runPipeline() error: %v", err)
		}
		if !ran {
			t.Errorf("runPipeline() returned ran=false, want true")
		}
		if pr.skipping {
			t.Errorf("skipping should be false after matching checkpoint")
		}
	})

	t.Run("skips non-matching checkpoints", func(t *testing.T) {
		pr := &pipelineRunner{
			runner:              &mockRunner{},
			config:              &container.Config{},
			skipUntilCheckpoint: "target-checkpoint",
			skipping:            true,
		}

		// Run a different checkpoint pipeline
		pipeline := &config.Pipeline{
			Checkpoint: "other-checkpoint",
		}

		ran, err := pr.runPipeline(ctx, pipeline)
		if err != nil {
			t.Fatalf("runPipeline() error: %v", err)
		}
		if !ran {
			t.Errorf("runPipeline() returned ran=false, want true")
		}
		if !pr.skipping {
			t.Errorf("skipping should still be true after non-matching checkpoint")
		}
	})

	t.Run("captures checkpoint when not skipping", func(t *testing.T) {
		tmpDir := t.TempDir()
		tarData := createTarData(t, map[string]string{"test.txt": "hello"})

		checkpointPath := func(name string) string {
			return filepath.Join(tmpDir, name+".tar")
		}

		pr := &pipelineRunner{
			runner:              &mockRunner{workspaceTarData: tarData},
			config:              &container.Config{},
			checkpointPath:      checkpointPath,
			skipUntilCheckpoint: "",
			skipping:            false,
		}

		pipeline := &config.Pipeline{
			Checkpoint: "capture-me",
		}

		ran, err := pr.runPipeline(ctx, pipeline)
		if err != nil {
			t.Fatalf("runPipeline() error: %v", err)
		}
		if !ran {
			t.Errorf("runPipeline() returned ran=false, want true")
		}

		// Verify checkpoint was actually captured
		if _, err := os.Stat(checkpointPath("capture-me")); err != nil {
			t.Errorf("checkpoint file should exist: %v", err)
		}
	})
}
