package main

import "os"

// private: Windows has no mode bits worth checking; the file lives in the
// user's own profile.
func private(os.FileInfo) bool { return true }
