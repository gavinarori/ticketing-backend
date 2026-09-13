package platform

import (
 "context"
 "errors"
 "io"
)

type ObjectStore interface { Put(context.Context, string, string, io.Reader) (Object, error); Get(context.Context, string) (io.ReadCloser, string, error); Delete(context.Context, string) error }
type Object struct { Key string `json:"key"`; ContentType string `json:"content_type"`; Size int64 `json:"size"`; Checksum string `json:"checksum"` }
type MemoryStore struct { objects map[string][]byte }
func NewMemoryStore() *MemoryStore { return &MemoryStore{objects: map[string][]byte{}} }
func (m *MemoryStore) Put(_ context.Context,key,contentType string,r io.Reader)(Object,error){ b,e:=io.ReadAll(r);if e!=nil{return Object{},e};m.objects[key]=b;return Object{Key:key,ContentType:contentType,Size:int64(len(b))},nil }
func (m *MemoryStore) Get(_ context.Context,key string)(io.ReadCloser,string,error){b,ok:=m.objects[key];if !ok{return nil,"",errors.New("object not found")};return io.NopCloser(bytesReader(b)),"application/pdf",nil}
func (m *MemoryStore) Delete(_ context.Context,key string)error{delete(m.objects,key);return nil}
type byteReader struct{ b []byte };func bytesReader(b []byte)*byteReader{return &byteReader{b:b}};func(r *byteReader)Read(p []byte)(int,error){if len(r.b)==0{return 0,io.EOF};n:=copy(p,r.b);r.b=r.b[n:];return n,nil}
