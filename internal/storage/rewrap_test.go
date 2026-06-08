package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// objectAndMeta reads the raw on-disk ciphertext blob and parsed sidecar meta.
func objectAndMeta(t *testing.T, dir, key string) ([]byte, localMeta) {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(key)))
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	meta, err := readMeta(filepath.Join(dir, filepath.FromSlash(key)) + localMetaSuffix)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	return blob, meta
}

// Re-wrapping migrates an object's data key to the current master key, changing
// ONLY the sidecar envelope (kid) — the ciphertext body is byte-identical — and
// lets the retired key be removed from the ring while the object stays readable.
func TestRewrapObject_MigratesKeyLeavesBody(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	kf := filepath.Join(t.TempDir(), "keys.json")
	mustWrite(t, kf, `{"primary":"v1","keys":{"v1":"pass-one"}}`)

	s1, err := NewLocal(LocalConfig{Root: dir, SSEKeyfile: kf})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("payload to be re-wrapped")
	if _, err := s1.Put(ctx, "default/o.txt", bytes.NewReader(body), int64(len(body)), PutOptions{}); err != nil {
		t.Fatal(err)
	}
	blobBefore, metaBefore := objectAndMeta(t, dir, "default/o.txt")
	if env, _ := parseEnvelope(metaBefore.Envelope); env.Kid != "v1" {
		t.Fatalf("want kid v1, got %q", env.Kid)
	}

	// Rotate to v2 (keep v1), reopen, re-wrap.
	mustWrite(t, kf, `{"primary":"v2","keys":{"v1":"pass-one","v2":"pass-two"}}`)
	s2, err := NewLocal(LocalConfig{Root: dir, SSEKeyfile: kf})
	if err != nil {
		t.Fatal(err)
	}
	done, err := s2.RewrapObject(ctx, "default/o.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("expected a re-wrap to occur")
	}

	blobAfter, metaAfter := objectAndMeta(t, dir, "default/o.txt")
	if !bytes.Equal(blobBefore, blobAfter) {
		t.Fatal("re-wrap must NOT change the ciphertext body")
	}
	if env, _ := parseEnvelope(metaAfter.Envelope); env.Kid != "v2" {
		t.Fatalf("want kid v2 after re-wrap, got %q", env.Kid)
	}
	if metaAfter.ETag != metaBefore.ETag || metaAfter.Size != metaBefore.Size {
		t.Fatal("re-wrap must not change etag/size")
	}

	// Idempotent: a second re-wrap is a no-op.
	if done, _ := s2.RewrapObject(ctx, "default/o.txt"); done {
		t.Fatal("second re-wrap should be a no-op")
	}

	// The retired key can now be dropped — object still reads back.
	mustWrite(t, kf, `{"primary":"v2","keys":{"v2":"pass-two"}}`)
	s3, err := NewLocal(LocalConfig{Root: dir, SSEKeyfile: kf})
	if err != nil {
		t.Fatal(err)
	}
	rc, _, err := s3.Get(ctx, "default/o.txt")
	if err != nil {
		t.Fatalf("read after retiring v1: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("mismatch after retiring v1: %q", got)
	}
}

func TestRewrapObject_SkipsPlaintextAndDisabled(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// SSE disabled → no-op even for a present object.
	plain, _ := NewLocal(LocalConfig{Root: dir})
	if _, err := plain.Put(ctx, "default/p.txt", bytes.NewReader([]byte("x")), 1, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if done, err := plain.RewrapObject(ctx, "default/p.txt"); err != nil || done {
		t.Fatalf("SSE-off rewrap should be no-op, got done=%v err=%v", done, err)
	}
	// SSE on, but the object is a pre-existing plaintext (no envelope) → skipped.
	withSSE, _ := NewLocal(LocalConfig{Root: dir, SSEKey: "k"})
	if done, err := withSSE.RewrapObject(ctx, "default/p.txt"); err != nil || done {
		t.Fatalf("plaintext rewrap should be no-op, got done=%v err=%v", done, err)
	}
}

// RewrapStale sweeps a whole backend: counts only the objects that actually moved
// to the current key, skipping plaintext and already-current ones.
func TestRewrapStale_Sweep(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	kf := filepath.Join(t.TempDir(), "keys.json")
	mustWrite(t, kf, `{"primary":"v1","keys":{"v1":"one"}}`)
	s1, _ := NewLocal(LocalConfig{Root: dir, SSEKeyfile: kf})
	for _, k := range []string{"default/a", "default/b", "default/c"} {
		if _, err := s1.Put(ctx, k, bytes.NewReader([]byte(k)), int64(len(k)), PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite(t, kf, `{"primary":"v2","keys":{"v1":"one","v2":"two"}}`)
	s2, _ := NewLocal(LocalConfig{Root: dir, SSEKeyfile: kf})
	// Write one fresh object already on v2 — it must not be counted.
	if _, err := s2.Put(ctx, "default/d", bytes.NewReader([]byte("d")), 1, PutOptions{}); err != nil {
		t.Fatal(err)
	}

	rep, err := RewrapStale(ctx, s2)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rewrapped != 3 {
		t.Fatalf("want 3 re-wrapped (a,b,c), got %d (scanned=%d failed=%d)", rep.Rewrapped, rep.Scanned, rep.Failed)
	}
	if rep.Scanned != 4 {
		t.Fatalf("want 4 scanned, got %d", rep.Scanned)
	}

	// All objects now decrypt with v2 only.
	mustWrite(t, kf, `{"primary":"v2","keys":{"v2":"two"}}`)
	s3, _ := NewLocal(LocalConfig{Root: dir, SSEKeyfile: kf})
	for _, k := range []string{"default/a", "default/b", "default/c", "default/d"} {
		rc, _, err := s3.Get(ctx, k)
		if err != nil {
			t.Fatalf("get %s after retiring v1: %v", k, err)
		}
		_ = rc.Close()
	}
}

// The env provider has no rotation: its objects carry no kid (== current id ""),
// so re-wrap is always a no-op.
func TestRewrapObject_EnvProviderIsNoOp(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, _ := NewLocal(LocalConfig{Root: dir, SSEKey: "p"})
	if _, err := s.Put(ctx, "default/e.txt", bytes.NewReader([]byte("x")), 1, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if done, err := s.RewrapObject(ctx, "default/e.txt"); err != nil || done {
		t.Fatalf("env-provider rewrap should be a no-op, got done=%v err=%v", done, err)
	}
}

// Migration: objects written by the single-key env provider (no kid) are re-wrapped
// onto a versioned keyfile id, so the env key can then be retired as the legacy slot.
func TestRewrapObject_EnvToKeyfileMigration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	const envPass = "legacy-pass"

	// Write under the env provider → envelope has no kid.
	s0, _ := NewLocal(LocalConfig{Root: dir, SSEKey: envPass})
	body := []byte("migrate me onto a versioned key")
	if _, err := s0.Put(ctx, "default/m.txt", bytes.NewReader(body), int64(len(body)), PutOptions{}); err != nil {
		t.Fatal(err)
	}
	_, m0 := objectAndMeta(t, dir, "default/m.txt")
	if e, _ := parseEnvelope(m0.Envelope); e.Kid != "" {
		t.Fatal("env-provider object should have no kid")
	}

	// Reopen with a keyfile (primary v1) + the env key as the legacy slot, re-wrap.
	kf := filepath.Join(t.TempDir(), "keys.json")
	mustWrite(t, kf, `{"primary":"v1","keys":{"v1":"versioned"}}`)
	s1, err := NewLocal(LocalConfig{Root: dir, SSEKeyfile: kf, SSEKey: envPass})
	if err != nil {
		t.Fatal(err)
	}
	done, err := s1.RewrapObject(ctx, "default/m.txt")
	if err != nil || !done {
		t.Fatalf("expected migration re-wrap, got done=%v err=%v", done, err)
	}
	_, m1 := objectAndMeta(t, dir, "default/m.txt")
	if e, _ := parseEnvelope(m1.Envelope); e.Kid != "v1" {
		t.Fatal("migrated object should now carry kid v1")
	}

	// The env/legacy key can now be dropped entirely — object still reads.
	s2, err := NewLocal(LocalConfig{Root: dir, SSEKeyfile: kf})
	if err != nil {
		t.Fatal(err)
	}
	rc, _, err := s2.Get(ctx, "default/m.txt")
	if err != nil {
		t.Fatalf("read after dropping the legacy env key: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("mismatch after migration: %q", got)
	}
}

// A temp file left behind by a crash mid-rename must not surface as an object.
func TestList_SkipsInternalTempFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, _ := NewLocal(LocalConfig{Root: dir})
	if _, err := s.Put(ctx, "default/real.txt", bytes.NewReader([]byte("x")), 1, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	// Simulate crash leftovers in the object's directory.
	objDir := filepath.Join(dir, "default")
	mustWrite(t, filepath.Join(objDir, ".meta-abc123"), "{}")
	mustWrite(t, filepath.Join(objDir, ".upload-xyz"), "garbage")

	res, err := s.List(ctx, "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range res.Objects {
		if strings.Contains(o.Key, ".meta-") || strings.Contains(o.Key, ".upload-") {
			t.Fatalf("List returned an internal temp file as an object: %q", o.Key)
		}
	}
	if len(res.Objects) != 1 || res.Objects[0].Key != "default/real.txt" {
		t.Fatalf("expected exactly the real object, got %+v", res.Objects)
	}
}

// A backend without SSE (no Rewrapper behavior) yields an empty report, not an error.
func TestRewrapStale_NoSSE(t *testing.T) {
	ctx := context.Background()
	s, _ := NewLocal(LocalConfig{Root: t.TempDir()})
	if _, err := s.Put(ctx, "default/x", bytes.NewReader([]byte("x")), 1, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	rep, err := RewrapStale(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rewrapped != 0 {
		t.Fatalf("SSE-off sweep should re-wrap nothing, got %d", rep.Rewrapped)
	}
}
