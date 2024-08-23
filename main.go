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
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

var (
	buildVersion            string = ""
	buildTime               string = ""
	debugEnv                string = os.Getenv("DEBUG")
	logFormatEnv            string = os.Getenv("LOG_FORMAT")
	debug                   bool
	defaultTags             map[string]string
	defaultAnnotationPrefix string = "k8s-pvc-tagger"
	annotationPrefix        string = "k8s-pvc-tagger"
	legacyAnnotationPrefix  string = "aws-ebs-tagger"
	watchNamespace          string
	tagFormat               string = "json"
	allowAllTags            bool
	cloud                   string
	copyLabels              []string

	promActionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "k8s_pvc_tagger_actions_total",
		Help: "The total number of PVCs tagged",
	}, []string{"status", "storageclass"})

	promIgnoredTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "k8s_pvc_tagger_pvc_ignored_total",
		Help: "The total number of PVCs ignored",
	}, []string{"storageclass"})

	promInvalidTagsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "k8s_pvc_tagger_invalid_tags_total",
		Help: "The total number of invalid tags found",
	}, []string{"storageclass"})

	promActionsLegacyTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "k8s_aws_ebs_tagger_actions_total",
		Help: "The total number of PVCs tagged",
	}, []string{"status"})

	promIgnoredLegacyTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "k8s_aws_ebs_tagger_pvc_ignored_total",
		Help: "The total number of PVCs ignored",
	})

	promInvalidTagsLegacyTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "k8s_aws_ebs_tagger_invalid_tags_total",
		Help: "The total number of invalid tags found",
	})
)

const (
	AWS   = "aws"
	AZURE = "azure"
	GCP   = "gcp"
)

func init() {
	if logFormatEnv == "" || strings.ToLower(logFormatEnv) == "json" {
		log.SetFormatter(&log.JSONFormatter{})
	}

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
	var err error
	var kubeconfig string
	var kubeContext string
	var region string
	var leaseLockName string
	var leaseLockNamespace string
	var leaseID string
	var defaultTagsString string
	var statusPort string
	var metricsPort string
	var copyLabelsString string

	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&kubeContext, "context", "", "the context to use")
	flag.StringVar(&region, "region", os.Getenv("AWS_REGION"), "the region")
	flag.StringVar(&leaseID, "lease-id", uuid.New().String(), "the holder identity name")
	flag.StringVar(&leaseLockName, "lease-lock-name", "k8s-pvc-tagger", "the lease lock resource name")
	flag.StringVar(&leaseLockNamespace, "lease-lock-namespace", os.Getenv("NAMESPACE"), "the lease lock resource namespace")
	flag.StringVar(&defaultTagsString, "default-tags", "", "Default tags to add to EBS/EFS volume")
	flag.StringVar(&tagFormat, "tag-format", "json", "Whether the tags are in json or csv format. Default: json")
	flag.StringVar(&annotationPrefix, "annotation-prefix", "k8s-pvc-tagger", "Annotation prefix to check")
	flag.StringVar(&watchNamespace, "watch-namespace", os.Getenv("WATCH_NAMESPACE"), "A specific namespace to watch (default is all namespaces)")
	flag.StringVar(&statusPort, "status-port", "8000", "The healthz port")
	flag.StringVar(&metricsPort, "metrics-port", "8001", "The prometheus metrics port")
	flag.BoolVar(&allowAllTags, "allow-all-tags", false, "Whether or not to allow any tag, even Kubernetes assigned ones, to be set")
	flag.StringVar(&cloud, "cloud", AWS, "The cloud provider (aws, gcp or azure)")
	flag.StringVar(&copyLabelsString, "copy-labels", "", "Comma-separated list of PVC labels to copy to volumes. Use '*' to copy all labels. (default \"\")")
	flag.Parse()

	if leaseLockName == "" {
		log.Fatalln("unable to get lease lock resource name (missing lease-lock-name flag).")
	}
	if leaseLockNamespace == "" {
		leaseLockNamespace = getCurrentNamespace()
		if leaseLockNamespace == "" {
			log.Fatalln("unable to get lease lock resource namespace (missing lease-lock-namespace flag).")
		}
	}

	switch cloud {
	case AWS:
		log.Infoln("Running in AWS mode")
		// Parse AWS_REGION environment variable.
		if len(region) == 0 {
			region, _ = getMetadataRegion()
			log.WithFields(log.Fields{"region": region}).Debugln("ec2Metadata region")
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
	case GCP:
		log.Infoln("Running in GCP mode")
	case AZURE:
		log.Infoln("Running in Azure mode")
	default:
		log.Fatalln("Cloud provider must be either aws or gcp")
	}

	defaultTags = make(map[string]string)
	if defaultTagsString != "" {
		log.Debugln("defaultTagsString:", defaultTagsString)
		if tagFormat == "csv" {
			defaultTags = parseCsv(defaultTagsString)
		} else {
			err := json.Unmarshal([]byte(defaultTagsString), &defaultTags)
			if err != nil {
				log.Fatalln("default-tags are not valid json key/value pairs:", err)
			}
		}
	}
	log.WithFields(log.Fields{"tags": defaultTags}).Infoln("Default Tags")

	if copyLabelsString != "" {
		copyLabels = parseCopyLabels(copyLabelsString)
		log.Infof("Copying PVC labels to tags: %v", copyLabels)
	}

	k8sClient, err = BuildClient(kubeconfig, kubeContext)
	if err != nil {
		log.Fatalln("Unable to create kubernetes client", err)
		os.Exit(1)
	}

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", statusHandler)
		server := &http.Server{
			Addr:              "0.0.0.0:" + statusPort,
			ReadHeaderTimeout: 3 * time.Second,
			Handler:           mux,
		}
		err := server.ListenAndServe()
		if err != nil {
			log.Errorln(err)
		}
	}()

	go func() {
		// Handle just the /metrics endpoint on the metrics port
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		server := &http.Server{
			Addr:              "0.0.0.0:" + metricsPort,
			ReadHeaderTimeout: 3 * time.Second,
			Handler:           mux,
		}
		err := server.ListenAndServe()
		if err != nil {
			log.Errorln(err)
		}
	}()

	run := func(ctx context.Context) {
		var namespaces []string
		if watchNamespace != "" {
			namespaces = strings.Split(watchNamespace, ",")
		} else {
			namespaces = append(namespaces, "")
		}
		for _, ns := range namespaces {
			go runWatchNamespaceTask(ctx, ns)
		}
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

func runWatchNamespaceTask(ctx context.Context, namespace string) {
	// Make the informer's channel here so we can close it when the
	// context is Done()
	ch := make(chan struct{})
	go watchForPersistentVolumeClaims(ch, namespace)

	<-ctx.Done()
	close(ch)
}

func parseCsv(value string) map[string]string {
	tags := make(map[string]string)
	for _, s := range strings.Split(value, ",") {
		if len(s) == 0 {
			continue
		}
		pairs := strings.SplitN(s, "=", 2)
		if len(pairs) != 2 {
			log.Errorln("invalid csv key/value pair. Skipping...")
			continue
		}
		k := strings.TrimSpace(pairs[0])
		v := strings.TrimSpace(pairs[1])
		if k == "" || v == "" {
			log.Errorln("invalid csv key/value pair. Skipping...")
			continue
		}
		tags[k] = v
	}

	return tags
}

func parseCopyLabels(copyLabelsString string) []string {
	if copyLabelsString == "*" {
		return []string{"*"}
	}
	// remove empty strings from final list, eg: "foo,,bar" -> ["foo" "bar"]:
	return strings.FieldsFunc(copyLabelsString, func(c rune) bool {
		return c == ','
	})
}
