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

// Package logsafe strips line breaks from values that reach a log line.
//
// Record names and types arrive in the request body, so they are attacker
// controlled as far as this process is concerned. A name carrying a newline
// forges a second log entry under the text formatter, which is the default.
// JSON output escapes them anyway, but the format is an operator's choice and
// log correctness should not depend on it.
package logsafe

import "strings"

// String removes the characters that end a log line.
//
// The replacement is unconditional. An "is it even needed" guard returning the
// input untouched reads as a free fast path, but it leaves a flow where the
// value reaches the log unchanged, so static analysis cannot call it sanitized
// and neither, strictly, can we. Two passes over a hostname cost nothing next
// to the write itself.
func String(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", ""), "\r", "")
}

// Err does the same for an error's message.
func Err(err error) string {
	if err == nil {
		return ""
	}

	return String(err.Error())
}
