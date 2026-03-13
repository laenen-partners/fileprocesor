package fileprocesor

import (
	"fmt"
	"strings"
)

// validateBucket checks that the bucket is non-empty and in the allowlist (if configured).
func (p *Processor) validateBucket(bucket string) error {
	if bucket == "" {
		return fmt.Errorf("bucket is required")
	}
	if p.allowedBuckets != nil && !p.allowedBuckets[bucket] {
		return fmt.Errorf("bucket %q is not allowed", bucket)
	}
	return nil
}

// validateKey checks that a key is non-empty and doesn't contain path traversal.
func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}
	if strings.Contains(key, "..") {
		return fmt.Errorf("key must not contain path traversal")
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

// validateFileRefPtr validates an optional FileRef pointer.
func (p *Processor) validateFileRefPtr(ref *FileRef, fieldName string) error {
	if ref == nil {
		return nil // optional
	}
	if err := p.validateBucket(ref.Bucket); err != nil {
		return fmt.Errorf("%s: %w", fieldName, err)
	}
	return validateKey(ref.Key)
}

// validateProcessInput validates a ProcessInput.
func (p *Processor) validateProcessInput(input ProcessInput) error {
	if len(input.Inputs) == 0 {
		return fmt.Errorf("at least one input is required")
	}
	if len(input.Operations) == 0 {
		return fmt.Errorf("at least one operation is required")
	}

	// Validate inputs: non-empty names, valid bucket/key, unique names.
	inputNames := make(map[string]bool, len(input.Inputs))
	for _, inp := range input.Inputs {
		if inp.Name == "" {
			return fmt.Errorf("input name is required")
		}
		if inputNames[inp.Name] {
			return fmt.Errorf("duplicate input name: %q", inp.Name)
		}
		inputNames[inp.Name] = true
		if err := p.validateFileRef(inp.Bucket, inp.Key); err != nil {
			return err
		}
	}

	// Validate operations: unique names, inputs reference known names, operation type set.
	knownNames := make(map[string]bool, len(input.Inputs)+len(input.Operations))
	for k := range inputNames {
		knownNames[k] = true
	}

	for _, op := range input.Operations {
		if op.Name == "" {
			return fmt.Errorf("operation name is required")
		}
		if knownNames[op.Name] && inputNames[op.Name] {
			return fmt.Errorf("operation name %q conflicts with input name", op.Name)
		}
		if !inputNames[op.Name] && knownNames[op.Name] {
			return fmt.Errorf("duplicate operation name: %q", op.Name)
		}

		if !hasOperationType(op) {
			return fmt.Errorf("operation %q has no type set", op.Name)
		}

		for _, ref := range op.Inputs {
			if !knownNames[ref] {
				return fmt.Errorf("operation %q references unknown input %q", op.Name, ref)
			}
		}

		knownNames[op.Name] = true
	}

	// Validate destinations reference known operation names.
	for name, dest := range input.Destinations {
		if !knownNames[name] {
			return fmt.Errorf("destination %q references unknown operation", name)
		}
		if err := p.validateFileRefPtr(&dest, "destination "+name); err != nil {
			return err
		}
	}

	return nil
}

func hasOperationType(op Operation) bool {
	return op.Scan != nil || op.ConvertToPDF != nil || op.MergePDFs != nil ||
		op.Thumbnail != nil || op.ExtractMarkdown != nil
}
