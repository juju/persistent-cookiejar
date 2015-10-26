// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cookiejar

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

// TODO:XXX: Redoc
func (j *Jar) Save() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.filename == "" {
		return errors.New("save called on non-loaded cookie jar")
	}
	if err := j.lock(); err != nil {
		return err
	}
	f, err := os.OpenFile(j.filename, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		j.unlock()
		return err
	}
	oldEntries := make(map[string]map[string]entry)
	err = readJSON(f, oldEntries)
	if err != nil && err != io.EOF {
		j.unlock()
		return err
	}
	// There was JSON parsed, merge overwriting found entries.
	if err != io.EOF {
		if err := f.Truncate(0); err != nil {
			j.unlock()
			return err
		}
		mergeEntries(oldEntries, j.entries)
		j.entries = oldEntries
	}
	err = writeJSON(f, j.entries)
	if errClose := f.Close(); err == nil {
		err = errClose
	}
	if errUnlock := j.unlock(); err == nil {
		err = errUnlock
	}
	return err
}

// TODO:XXX: Redoc
func (j *Jar) Load(path string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.filename = path
	if err := j.lock(); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		errUnlock := j.unlock()
		if os.IsNotExist(err) {
			err = errUnlock
		}
		return err
	}
	err = readJSON(f, j.entries)
	if errClose := f.Close(); err == nil {
		err = errClose
	}
	if errUnlock := j.unlock(); err == nil {
		err = errUnlock
	}
	return err
}

// WriteTo writes all the cookies in the jar to w
// as a JSON object.
func (j *Jar) WriteTo(w io.Writer) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return writeJSON(w, j.entries)
}

// TODO:XXX: Doc
func writeJSON(w io.Writer, m map[string]map[string]entry) error {
	// TODO don't store non-persistent cookies.
	encoder := json.NewEncoder(w)
	return encoder.Encode(m)
}

// ReadFrom reads all the cookies from r
// and stores them in the Jar.
func (j *Jar) ReadFrom(r io.Reader) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return readJSON(r, j.entries)
}

// TODO:XXX: Doc
func readJSON(r io.Reader, m map[string]map[string]entry) error {
	// TODO verification and expiry on read.
	decoder := json.NewDecoder(r)
	return decoder.Decode(&m)
}

// TODO:XXX: Doc
func (j *Jar) lock() error {
	var err error
	// Spin until lock is acquired.
	for {
		j.flock, err = os.OpenFile(j.filename+".lock", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0666)
		if err == os.ErrExist {
			time.Sleep(1 * time.Microsecond)
			continue
		}
		return err
	}
}

// TODO:XXX: Doc
func (j *Jar) unlock() error {
	name := j.flock.Name()
	err := j.flock.Close()
	if errRemove := os.Remove(name); err == nil {
		err = errRemove
	}
	return err
}

// TODO:XXX: Doc
func mergeEntries(dest, src map[string]map[string]entry) {
	for k0 := range src {
		if _, ok := src[k0]; !ok {
			dest[k0] = make(map[string]entry)
		}
		for k1 := range src[k0] {
			dest[k0][k1] = src[k0][k1]
		}
	}
}
