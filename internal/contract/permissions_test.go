// Package contract checks rules the stagd.api.v1 contract keeps that buf lint
// can't: every RPC declares the permission calling it takes (options.proto).
package contract

import (
	"regexp"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
)

// `<collection>:<action>`: lower snake_case, dots nesting a sub-collection.
var permission = regexp.MustCompile(`^[a-z][a-z_]*(\.[a-z][a-z_]*)*:[a-z][a-z_]*$`)

func TestEveryRPCDeclaresAPermission(t *testing.T) {
	methods := 0

	protoregistry.GlobalFiles.RangeFilesByPackage("stagd.api.v1", func(file protoreflect.FileDescriptor) bool {
		services := file.Services()

		for i := range services.Len() {
			service := services.Get(i)

			for j := range service.Methods().Len() {
				method := service.Methods().Get(j)
				methods++

				options, _ := method.Options().(*descriptorpb.MethodOptions)
				declared, _ := proto.GetExtension(options, apiv1.E_Permission).(string)

				if !permission.MatchString(declared) {
					t.Errorf("%s: permission %q: every RPC declares one, as <collection>:<action> (options.proto)", method.FullName(), declared)
				}
			}
		}

		return true
	})

	if methods == 0 {
		t.Fatal("no stagd.api.v1 RPCs registered")
	}
}
