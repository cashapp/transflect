// Package transflect contains utilities for getting FileDescriptorSet
// for given gRPC server address with enabled Reflection API.break
package transflect

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/jpillora/backoff"
	"github.com/pkg/errors"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	rpb "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// HTTPAnnotationPrefix url path prefix to use for missing HTTPRule.
var HTTPAnnotationPrefix = "/api"

// Base64Proto marshals and base64 encodes Proto message.
func Base64Proto(m proto.Message) (string, error) {
	b, err := proto.Marshal(m)
	if err != nil {
		return "", errors.Wrapf(err, "cannot proto marshal message %q", m)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// GetFileDescriptorSet returns the combined FileDescriptorProtos and
// transitive dependencies of all services running on address.
// The gRPC Reflection API must be available on the given address.
func GetFileDescriptorSet(ctx context.Context, addr string, opts ...grpc.DialOption) (*descriptorpb.FileDescriptorSet, []string, error) {
	conn, err := grpc.Dial(addr, opts...)
	if err != nil {
		return nil, nil, errors.Wrap(err, "cannot grpc dial")
	}
	defer conn.Close()
	client := rpb.NewServerReflectionClient(conn)
	return getFileDescriptorSetWithRetry(ctx, client, addr)
}

// LogAttempt is a hook for custom, possibly structured logging.
// It defaults to a no-op.
var LogAttempt = func(ctx context.Context, attempt int, err error) {}

func getFileDescriptorSetWithRetry(ctx context.Context, client rpb.ServerReflectionClient, addr string) (*descriptorpb.FileDescriptorSet, []string, error) {
	b := &backoff.Backoff{
		Min: 2 * time.Second,
		Max: 30 * time.Second,
	}
	for {
		fds, services, err := getFileDescriptorSet(ctx, client, addr)
		if err == nil {
			return fds, services, nil
		}
		if code, ok := getGRPCStatus(err); ok && code != codes.Unavailable {
			return nil, nil, errors.Wrapf(err, "gRPC error %d code not retryable", code)
		}
		attempt := int(b.Attempt())
		LogAttempt(ctx, attempt, err)
		if attempt > 15 {
			return nil, nil, errors.Wrap(err, "too many retries")
		}
		time.Sleep(b.Duration())
	}
}

func getGRPCStatus(err error) (codes.Code, bool) {
	var grpcStatus interface{ GRPCStatus() *status.Status }
	if errors.As(err, &grpcStatus) {
		return grpcStatus.GRPCStatus().Code(), true
	}
	return 0, false
}

func getFileDescriptorSet(ctx context.Context, client rpb.ServerReflectionClient, addr string) (*descriptorpb.FileDescriptorSet, []string, error) {
	services, err := getServices(ctx, client, addr)
	if err != nil {
		return nil, nil, err
	}

	files := map[string]*descriptorpb.FileDescriptorProto{}
	names := []string{}
	for _, service := range services {
		filesSlice, err := getFileDescriptorBySymbol(ctx, client, addr, service)
		if err != nil {
			return nil, nil, err
		}
		for _, file := range filesSlice {
			name := file.GetName()
			if files[name] == nil {
				AnnotateHTTPRule(file)
				files[name] = file
				names = append(names, name)
			}
		}
	}

	for _, name := range names {
		// Transitive dependencies may or may not have been already added to reflection responses in fileSlice above
		if err := addDependencies(ctx, client, addr, name, files); err != nil {
			return nil, nil, err
		}
	}
	sortedFiles := topoSort(names, files)
	fileDescriptorSet := &descriptorpb.FileDescriptorSet{File: sortedFiles}
	return fileDescriptorSet, services, nil
}

func addDependencies(ctx context.Context, client rpb.ServerReflectionClient, addr, name string, files map[string]*descriptorpb.FileDescriptorProto) error {
	for _, dep := range files[name].GetDependency() {
		if files[dep] != nil {
			continue
		}
		filesSlice, err := getFileDescriptorByFilename(ctx, client, addr, dep)
		if err != nil {
			return err
		}
		for _, file := range filesSlice {
			depName := file.GetName()
			if files[depName] != nil {
				continue
			}
			files[depName] = file
			if err := addDependencies(ctx, client, addr, depName, files); err != nil {
				return err
			}
		}
	}
	return nil
}

// topoSort sorts FileDesciptorProtos such that imported files (dependencies) come first.
// topoSort moves FileDescriptors from input map `files` to topographically sorted slice in return value.
func topoSort(names []string, files map[string]*descriptorpb.FileDescriptorProto) []*descriptorpb.FileDescriptorProto {
	var result []*descriptorpb.FileDescriptorProto
	for _, name := range names {
		if file := files[name]; file != nil {
			result = append(result, topoSort(file.Dependency, files)...)
			result = append(result, file)
			delete(files, name)
		}
	}
	return result
}

// AnnotateHTTPRule adds /api/<pkg>.<service>/<method> as HTTP URL path
// to  methods without http option provided in proto definition.
func AnnotateHTTPRule(fd *descriptorpb.FileDescriptorProto) {
	for _, service := range fd.GetService() {
		for _, method := range service.GetMethod() {
			opts := method.GetOptions()
			if proto.HasExtension(opts, annotations.E_Http) {
				continue
			}
			if method.Options == nil {
				method.Options = &descriptorpb.MethodOptions{}
			}

			path := fmt.Sprintf("%s/%s.%s/%s", HTTPAnnotationPrefix, fd.GetPackage(), service.GetName(), method.GetName())
			httpAnnotation := &annotations.HttpRule{
				Body: "*",
				Pattern: &annotations.HttpRule_Post{
					Post: path,
				},
			}
			proto.SetExtension(method.Options, annotations.E_Http, httpAnnotation)
		}
	}
}

func getFileDescriptorByFilename(ctx context.Context, client rpb.ServerReflectionClient, addr string, filename string) ([]*descriptorpb.FileDescriptorProto, error) {
	req := &rpb.ServerReflectionRequest{
		Host: addr,
		MessageRequest: &rpb.ServerReflectionRequest_FileByFilename{
			FileByFilename: filename,
		},
	}
	resp, err := doRefl(ctx, client, req)
	if err != nil {
		return nil, err
	}
	return decodeResponse(resp)
}

func getFileDescriptorBySymbol(ctx context.Context, client rpb.ServerReflectionClient, addr string, symbol string) ([]*descriptorpb.FileDescriptorProto, error) {
	req := &rpb.ServerReflectionRequest{
		Host: addr,
		MessageRequest: &rpb.ServerReflectionRequest_FileContainingSymbol{
			FileContainingSymbol: symbol,
		},
	}
	resp, err := doRefl(ctx, client, req)
	if err != nil {
		return nil, err
	}
	return decodeResponse(resp)
}

func decodeResponse(resp *rpb.ServerReflectionResponse) ([]*descriptorpb.FileDescriptorProto, error) {
	bs := resp.GetFileDescriptorResponse().FileDescriptorProto
	files := make([]*descriptorpb.FileDescriptorProto, 0, len(bs))
	for _, b := range bs {
		fd := &descriptorpb.FileDescriptorProto{}
		if err := proto.Unmarshal(b, fd); err != nil {
			return nil, errors.Wrap(err, "cannot unmarshal FileDescriptorProto")
		}
		files = append(files, fd)
	}
	return files, nil
}

func getServices(ctx context.Context, client rpb.ServerReflectionClient, addr string) ([]string, error) {
	req := &rpb.ServerReflectionRequest{
		Host:           addr,
		MessageRequest: &rpb.ServerReflectionRequest_ListServices{},
	}
	resp, err := doRefl(ctx, client, req)
	if err != nil {
		return nil, err
	}
	services := resp.GetListServicesResponse().GetService()
	names := make([]string, len(services))
	for i, service := range services {
		names[i] = service.Name
	}
	return names, nil
}

func doRefl(ctx context.Context, client rpb.ServerReflectionClient, req *rpb.ServerReflectionRequest) (*rpb.ServerReflectionResponse, error) {
	stream, err := client.ServerReflectionInfo(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "cannot setup reflection stream")
	}
	defer closeAndDrain(stream)
	if err := stream.Send(req); err != nil {
		return nil, errors.Wrap(err, "cannot send reflection request")
	}
	resp, err := stream.Recv()
	if err != nil {
		return nil, errors.Wrap(err, "cannot receive reflection response")
	}
	return resp, nil
}

func closeAndDrain(stream rpb.ServerReflection_ServerReflectionInfoClient) {
	_ = stream.CloseSend()
	for {
		if _, err := stream.Recv(); err != nil {
			return
		}
	}
}
