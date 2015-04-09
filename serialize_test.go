// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cookiejar

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

var serializeTestCookies = []*http.Cookie{{
	Name:       "foo",
	Value:      "bar",
	Path:       "/p",
	Domain:     "0.4.4.4",
	Expires:    time.Now(),
	RawExpires: time.Now().Format(time.RFC3339Nano),
	MaxAge:     99,
	Secure:     true,
	HttpOnly:   true,
	Raw:        "raw string",
	Unparsed:   []string{"x", "y", "z"},
}}

var serializeTestURL, _ = url.Parse("http://0.1.2.3/x")

func TestWriteTo(t *testing.T) {
	j, _ := New(nil)
	// Set an implausible cookie that nonetheless has
	// all fields set so that we're testing serialization of
	// all of them.
	j.SetCookies(serializeTestURL, serializeTestCookies)
	var buf bytes.Buffer
	err := j.WriteTo(&buf)
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	j1, _ := New(nil)
	err = j1.ReadFrom(&buf)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if !reflect.DeepEqual(j1.entries, j.entries) {
		t.Fatalf("entries differ after serialization")
	}
}

func TestLoadSave(t *testing.T) {
	d, err := ioutil.TempDir("", "")
	if err != nil {
		t.Fatalf("cannot make temp dir: %v", err)
	}
	defer os.RemoveAll(d)
	file := filepath.Join(d, "cookies")
	j, _ := New(nil)
	if err := j.Load(file); err != nil {
		t.Fatalf("cannot load from non-existent file: %v", err)
	}
	j.SetCookies(serializeTestURL, serializeTestCookies)
	if err := j.Save(); err != nil {
		t.Fatalf("cannot save: %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("file was not created")
	}
	j1, _ := New(nil)
	if err := j1.Load(file); err != nil {
		t.Fatalf("cannot load from non-existent file: %v", err)
	}
	if !reflect.DeepEqual(j1.entries, j.entries) {
		t.Fatalf("entries differ after serialization")
	}
}

func TestSaveWithNoLoad(t *testing.T) {
	j, _ := New(nil)
	err := j.Save()
	if err == nil {
		t.Fatalf("no error from Save without Load")
	}
}
