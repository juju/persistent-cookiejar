// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cookiejar
import (
	"io"
	"os"
	"encoding/json"
)

// SaveToFile is a convenience function that
// saves the cookies in j to a file at the given path
// using j.WriteTo.
func SaveToFile(j *Jar, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return j.WriteTo(f)
}

// LoadFromFile is a convenvience method that
// loads cookies from the file with the given name.
// using j.ReadFrom. If the file does not exist,
// no error will be returned and no cookies
// will be loaded.
func LoadFromFile(j *Jar, path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	return j.ReadFrom(f)
}

// WriteTo writes all the cookies in the jar to w
// as a JSON object.
func (j *Jar) WriteTo(w io.Writer) error {
	// TODO don't store non-persistent cookies.
	encoder := json.NewEncoder(w)
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := encoder.Encode(j.entries); err != nil {
		return err
	}
	return nil
}

// ReadFrom reads all the cookies from r
// and stores them in the Jar.
func (j *Jar) ReadFrom(r io.Reader) error {
	// TODO verification and expiry on read.
	decoder := json.NewDecoder(r)
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := decoder.Decode(&j.entries); err != nil {
		return err
	}
	return nil
}
