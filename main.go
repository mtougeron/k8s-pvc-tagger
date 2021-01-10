// Licensed to Michael Tougeron <github@e.tougeron.com> under
// one or more contributor license agreements. See the LICENSE
// file distributed with this work for additional information
// regarding copyright ownership.
// Michael Tougeron <github@e.tougeron.com> licenses this file
// to you under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

var (
	buildVersion string = ""
	buildTime    string = ""
	debugEnv     string = os.Getenv("DEBUG")
	debug        bool
	defaultTags  map[string]string
)

func init() {
	var err error
	if len(debugEnv) != 0 {
		debug, err = strconv.ParseBool(debugEnv)
		if err != nil {
			log.Fatalln("Failed to parse DEBUG Environment variable:", err.Error())
		}
	}

	if debug {
		log.SetLevel(log.DebugLevel)
	}

	// APP Build information
	log.Debugln("Application Version:", buildVersion)
	log.Debugln("Application Build Time:", buildTime)
}

func main() {
	var kubeconfig string
	var kubeContext string
	var region string
	var leaseLockName string
	var leaseLockNamespace string
	var leaseID string
	var defaultTagsString string

	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&kubeContext, "context", "", "the context to use")
	flag.StringVar(&region, "region", os.Getenv("AWS_REGION"), "the region")
	flag.StringVar(&leaseID, "lease-id", uuid.New().String(), "the holder identity name")
	flag.StringVar(&leaseLockName, "lease-lock-name", "k8s-aws-ebs-tagger", "the lease lock resource name")
	flag.StringVar(&leaseLockNamespace, "lease-lock-namespace", os.Getenv("NAMESPACE"), "the lease lock resource namespace")
	flag.StringVar(&defaultTagsString, "default-tags", "", "Default tags to add to EBS volume")
	flag.Parse()

	if leaseLockName == "" {
		log.Fatalln("unable to get lease lock resource name (missing lease-lock-name flag).")
	}
	if leaseLockNamespace == "" {
		log.Fatalln("unable to get lease lock resource namespace (missing lease-lock-namespace flag).")
	}

	if defaultTagsString != "" {
		err := json.Unmarshal([]byte(defaultTagsString), &defaultTags)
		if err != nil {
			log.Fatalln("default-tags are not valid json key/value pairs")
		}
	}

	// Parse AWS_REGION environment variable.
	if len(region) == 0 {
		region = defaultAWSRegion
	}
	ok, err := regexp.Match(regexpAWSRegion, []byte(region))
	if err != nil {
		log.Fatalln("Failed to parse AWS_REGION:", err.Error())
	}
	if !ok {
		log.Fatalln("Given AWS_REGION does not match AWS Region format.")
	}
	awsSession = createAWSSession(region)
	if awsSession == nil {
		err = fmt.Errorf("nil AWS session: %v", awsSession)
		if err != nil {
			log.Println(err.Error())
		}
		os.Exit(1)
	}

	k8sClient, err = buildClient(kubeconfig, kubeContext)
	if err != nil {
		log.Fatalln("Unable to create kubernetes client", err)
		os.Exit(1)
	}

	go func() {
		http.HandleFunc("/healthz", statusHandler)
		err := http.ListenAndServe("0.0.0.0:8080", nil)
		if err != nil {
			log.Errorln(err)
		}
	}()

	run := func(ctx context.Context) {
		watchForPersistentVolumeClaims()
	}

	// use a Go context so we can tell the leaderelection code when we
	// want to step down
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// listen for interrupts or the Linux SIGTERM signal and cancel
	// our context, which the leader election code will observe and
	// step down
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		log.Infoln("Received termination, signaling shutdown")
		cancel()
	}()

	// we use the Lease lock type since edits to Leases are less common
	// and fewer objects in the cluster watch "all Leases".
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      leaseLockName,
			Namespace: leaseLockNamespace,
		},
		Client: k8sClient.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: leaseID,
		},
	}

	// start the leader election code loop
	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock: lock,
		// IMPORTANT: you MUST ensure that any code you have that
		// is protected by the lease must terminate **before**
		// you call cancel. Otherwise, you could have a background
		// loop still running and another process could
		// get elected before your background loop finished, violating
		// the stated goal of the lease.
		ReleaseOnCancel: true,
		LeaseDuration:   60 * time.Second,
		RenewDeadline:   15 * time.Second,
		RetryPeriod:     5 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				run(ctx)
			},
			OnStoppedLeading: func() {
				log.Infoln("leader lost:", leaseID)
				os.Exit(0)
			},
			OnNewLeader: func(identity string) {
				// we're notified when new leader elected
				if identity == leaseID {
					return
				}
				log.Infoln("new leader elected:", identity)
			},
		},
	})

}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.WriteHeader(http.StatusNotImplemented)
		_, err := w.Write([]byte("method is not implemented"))
		if err != nil {
			log.Errorln("Cannot write status message:", err)
		}
		return
	}
	_, err := w.Write([]byte("OK"))
	if err != nil {
		log.Errorln("Cannot write status message:", err)
	}
}
