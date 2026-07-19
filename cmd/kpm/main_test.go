package main

import (
	"reflect"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	vf := map[string]bool{"asset": true, "name": true, "installed": true, "forge": true}
	cases := []struct {
		args     []string
		flags    []string
		posts    []string
	}{
		{
			args:  []string{"https://x/o/r", "--installed", "v1"},
			flags: []string{"--installed", "v1"}, posts: []string{"https://x/o/r"},
		},
		{
			args:  []string{"--name", "bee", "https://x/a/b", "--asset", "K*.tgz"},
			flags: []string{"--name", "bee", "--asset", "K*.tgz"}, posts: []string{"https://x/a/b"},
		},
		{
			args:  []string{"nh", "mid", "--all", "--reboot"},
			flags: []string{"--all", "--reboot"}, posts: []string{"nh", "mid"},
		},
		{
			args:  []string{"--asset=K*.tgz", "url"},
			flags: []string{"--asset=K*.tgz"}, posts: []string{"url"},
		},
		{
			args:  []string{"--", "-weird-positional"},
			flags: nil, posts: []string{"-weird-positional"},
		},
	}
	for _, c := range cases {
		f, p := splitArgs(c.args, vf)
		if !reflect.DeepEqual(f, c.flags) || !reflect.DeepEqual(p, c.posts) {
			t.Errorf("splitArgs(%v) = flags %v, pos %v; want flags %v, pos %v",
				c.args, f, p, c.flags, c.posts)
		}
	}
}
