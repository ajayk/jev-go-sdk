// Copyright 2026 Ajay Kemparaj
// SPDX-License-Identifier: Apache-2.0

package jev

import (
	"runtime"
)

// Version is the SDK version reported in the User-Agent and X-TypeSafe-SDK
// headers.
const Version = "0.2.0"

const sdkName = "jev-go-sdk"

// sdkIdentity is the value of the User-Agent and X-TypeSafe-SDK headers.
const sdkIdentity = sdkName + "/" + Version

// runtimeIdentity is the value of the X-TypeSafe-Runtime header.
var runtimeIdentity = runtime.Version() + " (" + runtime.GOOS + "; " + runtime.GOARCH + ")"
