package main

// sqlite.go — minimal read-only SQLite reader for game_logs.sqlite, Go
// standard library only (the console has no sqlite3). Table b-trees, overflow
// pages and records; no index lookups and no WAL (a pending -wal file is
// reported by `spm history audit` instead). The same walk serves every table.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
)

type sqliteDB struct {
	data     []byte
	pageSize int
	usable   int
}

func openSQLite(path string) (*sqliteDB, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 100 || !bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
		return nil, errors.New("not an SQLite database")
	}
	ps := int(binary.BigEndian.Uint16(data[16:18]))
	if ps == 1 {
		ps = 65536
	}
	if ps < 512 || ps&(ps-1) != 0 {
		return nil, fmt.Errorf("bad page size %d", ps)
	}
	if enc := binary.BigEndian.Uint32(data[56:60]); enc > 1 {
		return nil, errors.New("only UTF-8 databases are supported")
	}
	return &sqliteDB{data: data, pageSize: ps, usable: ps - int(data[20])}, nil
}

func (db *sqliteDB) page(n uint32) ([]byte, error) {
	start := int64(n-1) * int64(db.pageSize)
	if n == 0 || start+int64(db.pageSize) > int64(len(db.data)) {
		return nil, fmt.Errorf("page %d out of range", n)
	}
	return db.data[start : start+int64(db.pageSize)], nil
}

func varint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < 9 && i < len(b); i++ {
		if i == 8 {
			return v<<8 | uint64(b[i]), 9
		}
		v = v<<7 | uint64(b[i]&0x7f)
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return v, 0
}

func (db *sqliteDB) payload(p []byte, off int, size uint64) ([]byte, error) {
	u := db.usable
	x := u - 35
	if size <= uint64(x) {
		if off+int(size) > len(p) {
			return nil, errors.New("cell out of page")
		}
		return p[off : off+int(size)], nil
	}
	m := ((u-12)*32/255 - 23)
	k := m + int((size-uint64(m))%uint64(u-4))
	local := m
	if k <= x {
		local = k
	}
	if off+local+4 > len(p) {
		return nil, errors.New("cell out of page")
	}
	out := append([]byte(nil), p[off:off+local]...)
	next := binary.BigEndian.Uint32(p[off+local:])
	for guard := 0; uint64(len(out)) < size; guard++ {
		if next == 0 || guard > len(db.data)/db.pageSize {
			return nil, errors.New("broken overflow chain")
		}
		op, err := db.page(next)
		if err != nil {
			return nil, err
		}
		take := u - 4
		if rest := size - uint64(len(out)); rest < uint64(take) {
			take = int(rest)
		}
		out = append(out, op[4:4+take]...)
		next = binary.BigEndian.Uint32(op[0:4])
	}
	return out, nil
}

func (db *sqliteDB) walk(root uint32, depth int, fn func(rec []byte, rowid uint64) error) error {
	if depth > 32 {
		return errors.New("b-tree too deep")
	}
	p, err := db.page(root)
	if err != nil {
		return err
	}
	h := 0
	if root == 1 {
		h = 100
	}
	n := int(binary.BigEndian.Uint16(p[h+3:]))
	switch p[h] {
	case 0x0d: // table leaf
		for i := 0; i < n; i++ {
			off := int(binary.BigEndian.Uint16(p[h+8+2*i:]))
			size, a := varint(p[off:])
			rowid, b := varint(p[off+a:])
			if a == 0 || b == 0 {
				return errors.New("bad cell")
			}
			rec, err := db.payload(p, off+a+b, size)
			if err != nil {
				return err
			}
			if err := fn(rec, rowid); err != nil {
				return err
			}
		}
	case 0x05: // table interior
		for i := 0; i < n; i++ {
			off := int(binary.BigEndian.Uint16(p[h+12+2*i:]))
			if err := db.walk(binary.BigEndian.Uint32(p[off:]), depth+1, fn); err != nil {
				return err
			}
		}
		return db.walk(binary.BigEndian.Uint32(p[h+8:]), depth+1, fn)
	default:
		return fmt.Errorf("page %d is not a table page", root)
	}
	return nil
}

func record(rec []byte) ([]any, error) {
	hs, n := varint(rec)
	if n == 0 || hs > uint64(len(rec)) {
		return nil, errors.New("bad record header")
	}
	var types []uint64
	for pos := n; pos < int(hs); {
		t, k := varint(rec[pos:])
		if k == 0 {
			return nil, errors.New("bad record header")
		}
		types = append(types, t)
		pos += k
	}
	body := rec[hs:]
	vals := make([]any, 0, len(types))
	for _, t := range types {
		size := 0
		switch {
		case t >= 12:
			size = int(t-12) / 2
		case t >= 1 && t <= 4:
			size = int(t)
		case t == 5:
			size = 6
		case t == 6 || t == 7:
			size = 8
		}
		if size > len(body) {
			return nil, errors.New("record truncated")
		}
		v := body[:size]
		body = body[size:]
		switch {
		case t == 0:
			vals = append(vals, nil)
		case t >= 1 && t <= 6:
			var x int64
			for _, c := range v {
				x = x<<8 | int64(c)
			}
			if shift := 64 - 8*size; shift > 0 {
				x = x << shift >> shift
			}
			vals = append(vals, x)
		case t == 7:
			vals = append(vals, math.Float64frombits(binary.BigEndian.Uint64(v)))
		case t == 8:
			vals = append(vals, int64(0))
		case t == 9:
			vals = append(vals, int64(1))
		case t >= 13 && t%2 == 1:
			vals = append(vals, string(v))
		case t >= 12:
			vals = append(vals, append([]byte(nil), v...))
		default:
			return nil, fmt.Errorf("unsupported serial type %d", t)
		}
	}
	return vals, nil
}

func columns(sql string) []string {
	a, b := strings.Index(sql, "("), strings.LastIndex(sql, ")")
	if a < 0 || b <= a {
		return nil
	}
	var parts []string
	depth, start := 0, a+1
	for i := a + 1; i < b; i++ {
		switch sql[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, sql[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, sql[start:b])
	var cols []string
	for _, p := range parts {
		f := strings.Fields(p)
		if len(f) == 0 {
			continue
		}
		switch strings.ToLower(f[0]) {
		case "unique", "primary", "constraint", "check", "foreign":
			continue
		}
		cols = append(cols, strings.Trim(f[0], "\"`[]"))
	}
	return cols
}

// tableRows returns every row of a table as column name -> value, plus the
// rowid (which is also the value of an INTEGER PRIMARY KEY column).
func (db *sqliteDB) tableRows(table string) ([]map[string]any, error) {
	var root uint32
	var sql string
	err := db.walk(1, 0, func(rec []byte, _ uint64) error {
		v, err := record(rec)
		if err != nil || len(v) < 5 {
			return err
		}
		if t, _ := v[0].(string); t != "table" {
			return nil
		}
		if name, _ := v[1].(string); name == table {
			r, _ := v[3].(int64)
			root = uint32(r)
			sql, _ = v[4].(string)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if root == 0 {
		return nil, fmt.Errorf("no table %s", table)
	}
	cols := columns(sql)
	var rows []map[string]any
	err = db.walk(root, 0, func(rec []byte, rowid uint64) error {
		v, err := record(rec)
		if err != nil {
			return err
		}
		row := map[string]any{}
		for i, c := range cols {
			if i < len(v) {
				row[c] = v[i]
			} else {
				row[c] = nil
			}
		}
		row["rowid"] = int64(rowid)
		rows = append(rows, row)
		return nil
	})
	return rows, err
}
