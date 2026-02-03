package forge

import (
	"bytes"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum-optimism/optimism/op-deployer/pkg/deployer/artifacts"
)

type ioStruct struct {
	ID    uint8
	Data  []byte
	Slice []uint32
	Array [3]uint64
}

func TestMinimalSources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cl := NewClient(PathBinary())
	cl.Wd = projDir(t)

	// Build artifacts
	require.NoError(t, cl.Build(ctx))

	// Then copy them somewhere else
	tmpDir := t.TempDir()
	require.NoError(t, copyDir("testdata/testproject/out", path.Join(tmpDir, "out")))
	require.NoError(t, copyDir("testdata/testproject/cache", path.Join(tmpDir, "cache")))
	require.NoError(t, copyDir("testdata/testproject/script", path.Join(tmpDir, "script")))
	require.NoError(t, copyDir("testdata/testproject/foundry.toml", path.Join(tmpDir, "foundry.toml")))

	// Then see if we can successfully run a script
	cl.Wd = tmpDir
	caller := NewScriptCaller(
		cl,
		"script/Test.s.sol:TestScript",
		"runWithBytes(bytes)",
		&BytesScriptEncoder[ioStruct]{TypeName: "ioStruct"},
		&BytesScriptDecoder[ioStruct]{TypeName: "ioStruct"},
	)
	// It should not recompile since we included the cache.
	in := ioStruct{
		ID:    1,
		Data:  []byte{0x01, 0x02, 0x03, 0x04},
		Slice: []uint32{0x01, 0x02, 0x03, 0x04},
		Array: [3]uint64{0x01, 0x02, 0x03},
	}
	out, changed, err := caller(ctx, in)
	require.NoError(t, err)
	require.False(t, changed)
	require.EqualValues(t, ioStruct{
		ID:    2,
		Data:  in.Data,
		Slice: in.Slice,
		Array: in.Array,
	}, out)
}

// TestClient_Smoke smoke tests the Client by running the Version command on it.
func TestClient_Smoke(t *testing.T) {
	bin := PathBinary()
	cl := NewClient(bin)

	version, err := cl.Version(context.Background())
	require.NoError(t, err)
	require.Regexp(t, regexp.MustCompile(`\d+\.\d+\.\d+`), version.Semver)
	require.Regexp(t, regexp.MustCompile(`^[a-f0-9]+$`), version.SHA)
}

func TestClient_OutputRedirection(t *testing.T) {
	bin := PathBinary()
	cl := NewClient(bin)
	cl.Stdout = new(bytes.Buffer)

	_, err := cl.Version(context.Background())
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(cl.Stdout.(*bytes.Buffer).String(), "forge Version"))
}

func TestScriptCaller(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	bin := PathBinary()
	cl := NewClient(bin)
	cl.Wd = projDir(t)

	require.NoError(t, cl.Clean(ctx))
	caller := NewScriptCaller(
		cl,
		"script/Test.s.sol:TestScript",
		"runWithBytes(bytes)",
		&BytesScriptEncoder[ioStruct]{TypeName: "ioStruct"},
		&BytesScriptDecoder[ioStruct]{TypeName: "ioStruct"},
	)

	in := ioStruct{
		ID:    1,
		Data:  []byte{0x01, 0x02},
		Slice: []uint32{0x01, 0x02, 0x03, 0x04},
		Array: [3]uint64{0x01, 0x02, 0x03},
	}
	out, recompiled, err := caller(context.Background(), in)
	require.NoError(t, err)
	require.True(t, recompiled)
	require.EqualValues(t, ioStruct{
		ID:    2,
		Data:  in.Data,
		Slice: in.Slice,
		Array: in.Array,
	}, out)
	out, recompiled, err = caller(context.Background(), in)
	require.NoError(t, err)
	require.False(t, recompiled)
	require.EqualValues(t, ioStruct{
		ID:    2,
		Data:  in.Data,
		Slice: in.Slice,
		Array: in.Array,
	}, out)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(targetPath, 0755)
		}

		return copyFile(path, targetPath)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func projDir(t *testing.T) string {
	_, testFilename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Join(filepath.Dir(testFilename), "testdata", "testproject")
	absProjDir, err := filepath.Abs(dir)
	require.NoError(t, err)
	return absProjDir
}

func TestNewStandardClient_UniqueDirectories(t *testing.T) {
	// Create a temporary directory to use as workdir
	workdir := t.TempDir()

	// Create multiple clients and verify they get unique build output directories
	client1, err := NewStandardClient(workdir)
	require.NoError(t, err)
	defer os.RemoveAll(client1.buildOutDir)

	client2, err := NewStandardClient(workdir)
	require.NoError(t, err)
	defer os.RemoveAll(client2.buildOutDir)

	client3, err := NewStandardClient(workdir)
	require.NoError(t, err)
	defer os.RemoveAll(client3.buildOutDir)

	// All should share the same working directory (workdir)
	absWorkdir, err := filepath.Abs(workdir)
	require.NoError(t, err)
	require.Equal(t, absWorkdir, client1.Wd)
	require.Equal(t, absWorkdir, client2.Wd)
	require.Equal(t, absWorkdir, client3.Wd)

	// But build output directories should be unique
	require.NotEqual(t, client1.buildOutDir, client2.buildOutDir)
	require.NotEqual(t, client1.buildOutDir, client3.buildOutDir)
	require.NotEqual(t, client2.buildOutDir, client3.buildOutDir)

	// All build directories should exist
	require.DirExists(t, client1.buildOutDir)
	require.DirExists(t, client2.buildOutDir)
	require.DirExists(t, client3.buildOutDir)
}

func TestNewStandardClient_WithValidWorkdir(t *testing.T) {
	// Create a temporary directory to use as workdir
	workdir := t.TempDir()
	testFile := filepath.Join(workdir, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("test content"), 0644))

	client, err := NewStandardClient(workdir)
	require.NoError(t, err)
	defer os.RemoveAll(client.buildOutDir)

	// Client should use workdir directly as working directory
	absWorkdir, err := filepath.Abs(workdir)
	require.NoError(t, err)
	require.Equal(t, absWorkdir, client.Wd)

	// Verify we can access files in the working directory
	workdirFile := filepath.Join(client.Wd, "test.txt")
	require.FileExists(t, workdirFile)
	content, err := os.ReadFile(workdirFile)
	require.NoError(t, err)
	require.Equal(t, []byte("test content"), content)

	// Verify build output directory is unique and exists
	require.NotEmpty(t, client.buildOutDir)
	require.DirExists(t, client.buildOutDir)
}

func TestNewStandardClient_WithInvalidWorkdir(t *testing.T) {
	// Test with invalid/non-existent workdir - should fail
	_, err := NewStandardClient("/nonexistent/path")
	require.Error(t, err)
	require.Contains(t, err.Error(), "workdir does not exist or is not accessible")
}

func TestNewStandardClient_RegisteredForCleanup(t *testing.T) {
	// Get initial count of registered directories
	initialDirs := artifacts.GetCleanupDirs()
	initialCount := len(initialDirs)

	// Create a temporary directory to use as workdir
	workdir := t.TempDir()

	// Create a client
	client, err := NewStandardClient(workdir)
	require.NoError(t, err)
	defer os.RemoveAll(client.buildOutDir)

	// Check that the build directory was registered
	registeredDirs := artifacts.GetCleanupDirs()
	require.Greater(t, len(registeredDirs), initialCount)

	// Verify our build directory is in the list
	found := false
	for _, dir := range registeredDirs {
		if dir == client.buildOutDir {
			found = true
			break
		}
	}
	require.True(t, found, "Client build output directory should be registered for cleanup")
}

func TestNewStandardClient_ParallelInstances(t *testing.T) {
	// Test that multiple parallel instances don't conflict
	const numInstances = 10

	// Create a temporary directory to use as workdir
	workdir := t.TempDir()
	absWorkdir, err := filepath.Abs(workdir)
	require.NoError(t, err)

	clients := make([]*Client, numInstances)
	buildDirs := make(map[string]bool)

	for i := 0; i < numInstances; i++ {
		client, err := NewStandardClient(workdir)
		require.NoError(t, err)
		clients[i] = client
		defer os.RemoveAll(client.buildOutDir)

		// All instances should share the same working directory
		require.Equal(t, absWorkdir, client.Wd)

		// But build output directories should be unique
		require.False(t, buildDirs[client.buildOutDir], "Build directory should be unique: %s", client.buildOutDir)
		buildDirs[client.buildOutDir] = true
		require.DirExists(t, client.buildOutDir)
	}

	// All build directories should be different
	require.Len(t, buildDirs, numInstances)
}
