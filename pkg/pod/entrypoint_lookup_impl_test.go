/*
Copyright 2022 The Tekton Authors

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

package pod

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	remotetest "github.com/tektoncd/pipeline/test"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakeclient "k8s.io/client-go/kubernetes/fake"
)

const (
	username             = "foo"
	password             = "bar"
	imagePullSecretsName = "secret"
	nameSpace            = "ns"
)

type fakeHTTP struct {
	reg http.Handler
}

func newfakeHTTP() fakeHTTP {
	reg := registry.New()
	return fakeHTTP{
		reg: reg,
	}
}

func (f *fakeHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Request authentication for ping request.
	// For further reference see https://docs.docker.com/registry/spec/api/#api-version-check.
	if r.URL.Path == "/v2/" && r.Method == http.MethodGet {
		w.Header().Add("WWW-Authenticate", "basic")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// Check auth if we've fetching the image.
	if strings.HasPrefix(r.URL.Path, "/v2/task") && r.Method == "GET" {
		u, p, ok := r.BasicAuth()
		if !ok || username != u || password != p {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	// Default to open.
	f.reg.ServeHTTP(w, r)
}

func generateSecret(host string, username string, password string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      imagePullSecretsName,
			Namespace: nameSpace,
		},
		Type: corev1.SecretTypeDockercfg,
		Data: map[string][]byte{
			corev1.DockerConfigKey: []byte(
				fmt.Sprintf(`{%q: {"auth": %q}}`,
					host,
					base64.StdEncoding.EncodeToString([]byte(username+":"+password)),
				),
			),
		},
	}
}

func TestGetImageWithImagePullSecrets(t *testing.T) {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ftp := newfakeHTTP()
	s := httptest.NewServer(&ftp)
	defer s.Close()

	u, err := url.Parse(s.URL)
	if err != nil {
		t.Errorf("Parsing url with an error: %v", err)
	}

	task := &v1beta1.Task{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "tekton.dev/v1beta1",
			Kind:       "Task"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-create-image"},
	}

	ref, err := remotetest.CreateImageWithAnnotations(u.Host+"/task/test-create-image", remotetest.DefaultObjectAnnotationMapper, task)
	if err != nil {
		t.Errorf("uploading image failed unexpectedly with an error: %v", err)
	}

	imgRef, err := name.ParseReference(ref)
	if err != nil {
		t.Errorf("digest %s is not a valid reference: %v", ref, err)
	}

	for _, tc := range []struct {
		name             string
		basicSecret      *corev1.Secret
		imagePullSecrets []corev1.LocalObjectReference
		wantErr          bool
	}{{
		name:             "correct secret",
		basicSecret:      generateSecret(u.Host, username, password),
		imagePullSecrets: []corev1.LocalObjectReference{{Name: imagePullSecretsName}},
		wantErr:          false,
	}, {
		name:             "unauthorized secret",
		basicSecret:      generateSecret(u.Host, username, "wrong password"),
		imagePullSecrets: []corev1.LocalObjectReference{{Name: imagePullSecretsName}},
		wantErr:          true,
	}, {
		name:             "empty secret",
		basicSecret:      &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "foo"}},
		imagePullSecrets: []corev1.LocalObjectReference{{Name: imagePullSecretsName}},
		wantErr:          true,
	}, {
		name:             "no basic secret",
		basicSecret:      &corev1.Secret{},
		imagePullSecrets: []corev1.LocalObjectReference{{Name: imagePullSecretsName}},
		wantErr:          true,
	}, {
		name:             "no imagePullSecrets",
		basicSecret:      generateSecret(u.Host, username, password),
		imagePullSecrets: nil,
		wantErr:          true,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			client := fakeclient.NewSimpleClientset(&corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default",
					Namespace: nameSpace,
				},
			}, tc.basicSecret)

			entrypointCache, err := NewEntrypointCache(client)
			if err != nil {
				t.Errorf("Creating entrypointCache with an error: %v", err)
			}

			i, err := entrypointCache.get(ctx, imgRef, nameSpace, "", tc.imagePullSecrets)
			if (err != nil) != tc.wantErr {
				t.Fatalf("get() = %+v, %v, wantErr %t", i, err, tc.wantErr)
			}

		})

	}

}
