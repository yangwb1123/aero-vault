package repository

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// NewVersionID returns a fresh, collision-resistant version identifier. It is
// exported so the service layer can assign the authoritative version_id up
// front and reuse it as the per-version storage-key suffix, keeping storage_key
// and version_id consistent (InsertObjectVersion honours a preset VersionID).
func NewVersionID() string {
	return uuidLike()
}

// --- Scanners ---

type rowScanner interface {
	Scan(dest ...any) error
}

func scanObject(row rowScanner) (Object, error) {
	var (
		obj       Object
		metaRaw   []byte
		tagRaw    []byte
		createdT  flexTime
		updatedT  flexTime
		deletedT  flexNullTime
		lockedT   flexNullTime
		tombstone bool
	)
	err := row.Scan(
		&obj.ID, &obj.TenantID, &obj.Bucket, &obj.Key, &obj.VersionID, &obj.Backend, &obj.StorageKey,
		&obj.Size, &obj.ETag, &obj.ContentType,
		&metaRaw, &tagRaw, &obj.StorageClass,
		&createdT, &updatedT, &deletedT, &lockedT, &tombstone,
	)
	if err != nil {
		return Object{}, err
	}
	obj.VersionTombstone = tombstone
	obj.Metadata, _ = unmarshalKV(metaRaw)
	obj.Tags, _ = unmarshalKV(tagRaw)
	obj.CreatedAt = createdT.Time
	obj.UpdatedAt = updatedT.Time
	if deletedT.Valid {
		t := deletedT.Time
		obj.DeletedAt = &t
	}
	if lockedT.Valid {
		t := lockedT.Time
		obj.LockedUntil = &t
	}
	return obj, nil
}

// uuidLike returns a 36-char hex string; we keep it dep-free here since the
// rest of the package only needs uniqueness, not RFC-4122 compliance.
func uuidLike() string {
	const hex = "0123456789abcdef"
	now := time.Now().UnixNano()
	var b [16]byte
	_, _ = cryptoRandRead(b[:])
	binaryPutUint64(b[:8], uint64(now))
	var out [36]byte
	idx := 0
	for i, x := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out[idx] = '-'
			idx++
		}
		out[idx] = hex[x>>4]
		out[idx+1] = hex[x&0x0f]
		idx += 2
	}
	return string(out[:idx])
}

func scanChunks(rows *sql.Rows) ([]Chunk, error) {
	var out []Chunk
	for rows.Next() {
		var (
			c        Chunk
			embedRaw []byte
			createdT flexTime
		)
		if err := rows.Scan(&c.ID, &c.ObjectID, &c.TenantID, &c.Bucket, &c.ObjectKey, &c.Seq, &c.Content, &embedRaw, &c.Dim, &c.EmbedModel, &createdT); err != nil {
			return nil, err
		}
		c.CreatedAt = createdT.Time
		c.Embedding = decodeEmbedding(embedRaw)
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanUsages(rows *sql.Rows) ([]Usage, error) {
	var out []Usage
	for rows.Next() {
		var (
			u        Usage
			cidRaw   []byte
			oidRaw   []byte
			createdT flexTime
		)
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Caller, &u.Query, &cidRaw, &oidRaw, &u.RequestID, &createdT, &u.Model, &u.PromptTokens, &u.CompletionTokens, &u.TotalTokens, &u.LatencyMs, &u.CostMicros); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(cidRaw, &u.ChunkIDs)
		_ = json.Unmarshal(oidRaw, &u.ObjectIDs)
		u.CreatedAt = createdT.Time
		out = append(out, u)
	}
	return out, rows.Err()
}

// --- Helpers ---

func defaultTenant(t string) string {
	if t == "" {
		return "default"
	}
	return t
}

func unmarshalKV(b []byte) (map[string]string, error) {
	if len(b) == 0 {
		return map[string]string{}, nil
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func jsonOrEmpty(m map[string]string) ([]byte, error) {
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// encodeEmbedding packs []float32 as little-endian bytes; nil → nil so the
// column can stay NULL until an embedder runs.
func encodeEmbedding(v []float32) []byte {
	if len(v) == 0 {
		return nil
	}
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func decodeEmbedding(b []byte) []float32 {
	if len(b) == 0 || len(b)%4 != 0 {
		return nil
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

func norm(v []float32) float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return float32(math.Sqrt(s))
}

func cosine(a, b []float32, aNorm float32) float32 {
	var dot, bSq float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		bSq += float64(b[i]) * float64(b[i])
	}
	bNorm := math.Sqrt(bSq)
	if bNorm == 0 || aNorm == 0 {
		return 0
	}
	return float32(dot / (float64(aNorm) * bNorm))
}

// flexTime accepts time.Time, []byte, or string (RFC3339[Nano]).
type flexTime struct {
	Time time.Time
}

func (t *flexTime) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		return nil
	case time.Time:
		t.Time = v
	case []byte:
		return t.parse(string(v))
	case string:
		return t.parse(v)
	default:
		return fmt.Errorf("flexTime: unsupported %T", src)
	}
	return nil
}

func (t *flexTime) parse(s string) error {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, s); err == nil {
			t.Time = parsed
			return nil
		}
	}
	if ns, err := strconv.ParseInt(s, 10, 64); err == nil {
		t.Time = time.Unix(0, ns)
		return nil
	}
	return fmt.Errorf("flexTime: cannot parse %q", s)
}

type flexNullTime struct {
	Time  time.Time
	Valid bool
}

func (t *flexNullTime) Scan(src any) error {
	if src == nil {
		return nil
	}
	var f flexTime
	if err := f.Scan(src); err != nil {
		return err
	}
	t.Time = f.Time
	t.Valid = !f.Time.IsZero()
	return nil
}
