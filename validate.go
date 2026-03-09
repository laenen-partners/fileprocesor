package fileprocesor

import (
	"fmt"
	"strings"

	"connectrpc.com/connect"

	fpv1 "github.com/laenen-partners/fileprocesor/gen/fileprocessor/v1"
)

// validateBucket checks that the bucket is non-empty and in the allowlist (if configured).
func (p *Processor) validateBucket(bucket string) error {
	if bucket == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("bucket is required"))
	}
	if p.allowedBuckets != nil && !p.allowedBuckets[bucket] {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("bucket %q is not allowed", bucket))
	}
	return nil
}

// validateKey checks that a key is non-empty and doesn't contain path traversal.
func validateKey(key string) error {
	if key == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key is required"))
	}
	if strings.Contains(key, "..") {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("key must not contain path traversal"))
	}
	return nil
}

// validateFileRef validates a bucket/key pair.
func (p *Processor) validateFileRef(bucket, key string) error {
	if err := p.validateBucket(bucket); err != nil {
		return err
	}
	return validateKey(key)
}

// validateFileRefMsg validates a FileRef proto message.
func (p *Processor) validateFileRefMsg(ref *fpv1.FileRef, fieldName string) error {
	if ref == nil {
		return nil // optional
	}
	if err := p.validateBucket(ref.Bucket); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s: %w", fieldName, err))
	}
	return validateKey(ref.Key)
}

// validateProcessRequest validates a Process RPC request.
func (p *Processor) validateProcessRequest(msg *fpv1.ProcessRequest) error {
	if len(msg.Inputs) == 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at least one input is required"))
	}
	if len(msg.Operations) == 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at least one operation is required"))
	}

	// Validate inputs: non-empty names, valid bucket/key, unique names.
	inputNames := make(map[string]bool, len(msg.Inputs))
	for _, inp := range msg.Inputs {
		if inp.Name == "" {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("input name is required"))
		}
		if inputNames[inp.Name] {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("duplicate input name: %q", inp.Name))
		}
		inputNames[inp.Name] = true
		if err := p.validateFileRef(inp.Bucket, inp.Key); err != nil {
			return err
		}
	}

	// Validate operations: unique names, inputs reference known names, operation type set.
	knownNames := make(map[string]bool, len(msg.Inputs)+len(msg.Operations))
	for k := range inputNames {
		knownNames[k] = true
	}

	for _, op := range msg.Operations {
		if op.Name == "" {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("operation name is required"))
		}
		if knownNames[op.Name] && inputNames[op.Name] {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("operation name %q conflicts with input name", op.Name))
		}
		if !inputNames[op.Name] && knownNames[op.Name] {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("duplicate operation name: %q", op.Name))
		}

		if op.Op == nil {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("operation %q has no type set", op.Name))
		}

		for _, ref := range op.Inputs {
			if !knownNames[ref] {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("operation %q references unknown input %q", op.Name, ref))
			}
		}

		knownNames[op.Name] = true
	}

	// Validate destinations reference known operation names.
	for name, dest := range msg.Destinations {
		if !knownNames[name] {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("destination %q references unknown operation", name))
		}
		if err := p.validateFileRefMsg(dest, "destination "+name); err != nil {
			return err
		}
	}

	return nil
}
