# Business Logic Extraction Refactoring

**Date**: 2026-10-20  
**Author**: Xiaoliu  
**Status**: ✅ Completed

## Overview

Extracted business validation and canonicalization logic from the `api/inference/v1` protobuf package into a dedicated `internal/biz/inference` business logic package. This improves architectural separation and makes the codebase easier to maintain.

## Motivation

The original structure mixed protobuf-generated code with business logic:

```
api/inference/v1/
├── inference.proto              # Protocol definition
├── inference.pb.go              # Generated
├── inference_grpc.pb.go         # Generated
├── business_validation.go       # Business logic ❌
├── business_payload.go          # Business logic ❌
└── business_payload_test.go     # Business logic ❌
```

This violated the principle that `api/` should only contain protocol definitions and generated code, not business rules.

## Changes

### 1. Created New Business Logic Package

Created `internal/biz/inference/` and moved:

- `business_validation.go` → `internal/biz/inference/business_validation.go`
- `business_payload.go` → `internal/biz/inference/business_payload.go`
- `business_payload_test.go` → `internal/biz/inference/business_payload_test.go`

### 2. Updated Package Declarations

Changed from:
```go
package inferencev1
```

To:
```go
package inference

import inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
```

### 3. Updated Type References

All protobuf type references now use the `inferencev1.` prefix:

```go
// Before (in api/inference/v1/)
func ValidateBusinessPayload(req *CreateInferenceServiceRequest) error

// After (in internal/biz/inference/)
func ValidateBusinessPayload(req *inferencev1.CreateInferenceServiceRequest) error
```

### 4. Updated Call Sites

Updated imports and calls in:

- `internal/service/inference.go`
- `internal/service/managed_gpu.go`

Changed from:
```go
import inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
...
if err := inferencev1.ValidateBusinessPayload(req); err != nil {
```

To:
```go
import (
    inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
    inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
)
...
if err := inferencebiz.ValidateBusinessPayload(req); err != nil {
```

### 5. Kept `validate.go` in API Package

The `validate.go` file defines a `Validate()` method on the `Bootstrap` protobuf type. In Go, methods can only be defined in the same package as the type, so this file stayed in `api/inference/v1/`.

## Final Structure

```
api/inference/v1/
├── inference.proto              # Protocol definition
├── inference.pb.go              # Generated
├── inference_grpc.pb.go         # Generated
└── validate.go                  # Config validation (method extension)

internal/biz/inference/
├── business_validation.go       # Business validation rules
├── business_payload.go          # Canonicalization & hashing
├── business_payload_test.go     # Business logic tests
├── command.go                   # Command types
├── create.go                    # Create logic
├── update.go                    # Update logic
├── read.go                      # Read logic
├── operation.go                 # Operation state machine
├── runner.go                    # Operation runner
└── ...                          # Other business logic
```

## Testing

All tests pass:

```bash
$ go test ./... -short
ok      github.com/liangzai006/ani-inference-service/internal/biz/inference     0.010s
ok      github.com/liangzai006/ani-inference-service/internal/service           0.009s
# ... all other packages pass
```

## Impact

### Before

- ❌ Business logic mixed with protobuf definitions
- ❌ Harder to find business validation rules
- ❌ Violated architectural layering

### After

- ✅ Clean separation: `api/` = protocol, `internal/biz/` = business logic
- ✅ Business logic grouped together in `internal/biz/inference/`
- ✅ Follows standard Go project layout
- ✅ No functional changes, pure refactoring

## Migration Guide

If you were importing business logic functions:

```go
// Before
import inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
err := inferencev1.ValidateBusinessPayload(req)
digest, _ := inferencev1.BusinessPayloadDigest(req)

// After
import (
    inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
    inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
)
err := inferencebiz.ValidateBusinessPayload(req)
digest, _ := inferencebiz.BusinessPayloadDigest(req)
```

## Related Files

- Refactoring plan: `/root/kubercon/ani-inference-service/REFACTOR_PLAN.md`
- Modified files: 6 files
- New files: 3 files (moved from `api/`)
- Deleted files: 3 files (from `api/`, now in `internal/biz/`)
