package forge

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ethereum-optimism/optimism/op-deployer/pkg/deployer/artifacts"
)

var (
	versionRegexp = regexp.MustCompile(`(?i)forge version: (.*)\ncommit sha: ([a-f0-9]+)\n`)
	sigilRegexp   = regexp.MustCompile(`(?i)== Return ==\n0: bytes 0x([a-f0-9]+)\n`)
)

type VersionInfo struct {
	Semver string
	SHA    string
}

type Client struct {
	Binary      Binary
	Stdout      io.Writer
	Stderr      io.Writer
	Wd          string
	buildOutDir string // Base directory containing unique build output subdirectories
}

// copyArtifactsDir recursively copies a directory from src to dst
func copyArtifactsDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("failed to read source directory: %w", err)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyArtifactsDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			srcFile, err := os.Open(srcPath)
			if err != nil {
				return fmt.Errorf("failed to open source file: %w", err)
			}

			dstFile, err := os.Create(dstPath)
			if err != nil {
				srcFile.Close()
				return fmt.Errorf("failed to create destination file: %w", err)
			}

			if _, err := io.Copy(dstFile, srcFile); err != nil {
				srcFile.Close()
				dstFile.Close()
				return fmt.Errorf("failed to copy file: %w", err)
			}

			srcFile.Close()
			dstFile.Close()
		}
	}

	return nil
}

func NewStandardClient(workdir string) (*Client, error) {
	forgeBinary, err := NewStandardBinary()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize forge binary: %w", err)
	}
	if err := forgeBinary.Ensure(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to ensure forge binary: %w", err)
	}

	forgeClient := NewClient(forgeBinary)

	// Validate that workdir is a valid directory
	if workdir == "" {
		return nil, fmt.Errorf("workdir cannot be empty")
	}
	info, err := os.Stat(workdir)
	if err != nil {
		return nil, fmt.Errorf("workdir does not exist or is not accessible: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workdir is not a directory: %s", workdir)
	}

	// Convert workdir to absolute path
	absWorkdir, err := filepath.Abs(workdir)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path for workdir: %w", err)
	}

	// Use the workdir directly as Forge's working directory
	// Multiple instances can share the same source directory (read-only)
	// Build outputs are isolated via --out and --cache-path flags
	forgeClient.Wd = absWorkdir

	// Create unique build output directories to avoid conflicts when multiple
	// op-deployer instances run in parallel
	uuidBytes := make([]byte, 16)
	if _, err := rand.Read(uuidBytes); err != nil {
		return nil, fmt.Errorf("failed to generate unique ID: %w", err)
	}
	uuidStr := fmt.Sprintf("%x-%x-%x-%x-%x", uuidBytes[0:4], uuidBytes[4:6], uuidBytes[6:8], uuidBytes[8:10], uuidBytes[10:16])
	uniqueBuildBaseDir := filepath.Join(os.TempDir(), "forge-build-"+uuidStr)
	uniqueBuildOutDir := filepath.Join(uniqueBuildBaseDir, "out")
	uniqueCacheDir := filepath.Join(uniqueBuildBaseDir, "cache")
	if err := os.MkdirAll(uniqueBuildOutDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create unique build out dir: %w", err)
	}
	if err := os.MkdirAll(uniqueCacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create unique cache dir: %w", err)
	}

	// Register build directory for cleanup
	artifacts.RegisterForCleanup(uniqueBuildBaseDir)

	// Store the build base directory for build output paths
	forgeClient.buildOutDir = uniqueBuildBaseDir
	fmt.Printf("Forge client working directory: %s\n", forgeClient.Wd)
	fmt.Printf("Forge client build output directory: %s\n", uniqueBuildOutDir)

	return forgeClient, nil
}

func NewClient(binary Binary) *Client {
	return &Client{
		Binary: binary,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
}

func (c *Client) Version(ctx context.Context) (VersionInfo, error) {
	buf := new(bytes.Buffer)
	if err := c.execCmd(ctx, buf, io.Discard, "--version"); err != nil {
		return VersionInfo{}, fmt.Errorf("failed to run forge version command: %w", err)
	}
	outputStr := buf.String()
	matches := versionRegexp.FindAllStringSubmatch(outputStr, -1)
	if len(matches) != 1 || len(matches[0]) != 3 {
		return VersionInfo{}, fmt.Errorf("failed to find forge version in output:\n%s", outputStr)
	}
	return VersionInfo{
		Semver: matches[0][1],
		SHA:    matches[0][2],
	}, nil
}

func (c *Client) Build(ctx context.Context, opts ...string) error {
	cliOpts := []string{"build"}
	// Add unique build output directories to avoid conflicts
	if c.buildOutDir != "" {
		cliOpts = append(cliOpts, "--out", filepath.Join(c.buildOutDir, "out"))
		cliOpts = append(cliOpts, "--cache-path", filepath.Join(c.buildOutDir, "cache"))
	}
	cliOpts = append(cliOpts, opts...)
	return c.execCmd(ctx, io.Discard, io.Discard, cliOpts...)
}

func (c *Client) Clean(ctx context.Context, opts ...string) error {
	return c.execCmd(ctx, io.Discard, io.Discard, append([]string{"clean"}, opts...)...)
}

func (c *Client) RunScript(ctx context.Context, script string, sig string, args []byte, opts ...string) (string, error) {
	buf := new(bytes.Buffer)
	cliOpts := []string{"script"}
	cliOpts = append(cliOpts, opts...)
	// Add unique build output directories to avoid conflicts
	if c.buildOutDir != "" {
		cliOpts = append(cliOpts, "--out", filepath.Join(c.buildOutDir, "out"))
		cliOpts = append(cliOpts, "--cache-path", filepath.Join(c.buildOutDir, "cache"))
	}
	cliOpts = append(cliOpts, "--sig", sig, script, "0x"+hex.EncodeToString(args))
	if err := c.execCmd(ctx, buf, io.Discard, cliOpts...); err != nil {
		return "", fmt.Errorf("failed to execute forge script: %w", err)
	}
	return buf.String(), nil
}

func (c *Client) VerifyContract(ctx context.Context, opts ...string) (string, error) {
	buf := new(bytes.Buffer)
	cliOpts := []string{"verify-contract"}
	cliOpts = append(cliOpts, opts...)
	if err := c.execCmd(ctx, buf, buf, cliOpts...); err != nil {
		return buf.String(), fmt.Errorf("failed to verify contract: %w", err)
	}
	return buf.String(), nil
}

func (c *Client) execCmd(ctx context.Context, stdout io.Writer, stderr io.Writer, args ...string) error {
	if err := c.Binary.Ensure(ctx); err != nil {
		return fmt.Errorf("failed to ensure binary: %w", err)
	}

	cmd := exec.CommandContext(ctx, c.Binary.Path(), args...)
	cStdout := c.Stdout
	if cStdout == nil {
		cStdout = os.Stdout
	}
	cStderr := c.Stderr
	if cStderr == nil {
		cStderr = os.Stderr
	}

	mwStdout := io.MultiWriter(cStdout, stdout)
	mwStderr := io.MultiWriter(cStderr, stderr)
	cmd.Stdout = mwStdout
	cmd.Stderr = mwStderr
	cmd.Dir = c.Wd
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run forge command: %w", err)
	}
	return nil
}

type ScriptCallEncoder[I any] interface {
	Encode(I) ([]byte, error)
}

type ScriptCallDecoder[O any] interface {
	Decode(raw []byte) (O, error)
}

// ScriptCaller is a function that calls a forge script
// Ouputs:
// - Return value of the script (decoded into go type)
// - Bool indicating if the script was recompiled (mostly used for testing)
// - Error if the script fails to run
type ScriptCaller[I any, O any] func(ctx context.Context, input I, opts ...string) (O, bool, error)

func NewScriptCaller[I any, O any](client *Client, script string, sig string, encoder ScriptCallEncoder[I], decoder ScriptCallDecoder[O]) ScriptCaller[I, O] {
	return func(ctx context.Context, input I, opts ...string) (O, bool, error) {
		var out O
		encArgs, err := encoder.Encode(input)
		if err != nil {
			return out, false, fmt.Errorf("failed to encode forge args: %w", err)
		}
		rawOut, err := client.RunScript(ctx, script, sig, encArgs, opts...)
		if err != nil {
			return out, false, fmt.Errorf("failed to run forge script: %w", err)
		}
		sigilMatches := sigilRegexp.FindAllStringSubmatch(rawOut, -1)
		if len(sigilMatches) != 1 || len(sigilMatches[0]) != 2 {
			return out, false, fmt.Errorf("failed to find forge return value in output:\n%s", rawOut)
		}
		decoded, err := hex.DecodeString(sigilMatches[0][1])
		if err != nil {
			return out, false, fmt.Errorf("failed to decode forge return value %s: %w", sigilMatches[0][1], err)
		}
		out, err = decoder.Decode(decoded)
		if err != nil {
			return out, false, fmt.Errorf("failed to decode forge output: %w", err)
		}
		return out, strings.Contains(rawOut, "Compiler run successful!"), nil
	}
}
