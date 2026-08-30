/*
Copyright 2026 Jeremy Delgado.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package logsafe

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestString(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "an ordinary hostname is untouched", in: "www.example.com", want: "www.example.com"},
		{name: "a forged second line is flattened", in: "www.example.com\nlevel=info msg=\"granted\"", want: "www.example.comlevel=info msg=\"granted\""},
		{name: "carriage return too", in: "a\r\nb", want: "ab"},
		{name: "empty", in: "", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, String(tc.in))
		})
	}
}

func TestErr(t *testing.T) {
	assert.Equal(t, "", Err(nil))
	// The break is removed, not replaced by a space: only forged input carries
	// one, so the tokens run together in exactly the case nobody is reading for
	// prose.
	assert.Equal(t, "boomhere", Err(errors.New("boom\nhere")))
}
