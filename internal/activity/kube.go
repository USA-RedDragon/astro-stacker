package activity

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	saDir = "/var/run/secrets/kubernetes.io/serviceaccount/"
)

// ErrNotInCluster is a process with no service account token.
var ErrNotInCluster = errors.New("no Kubernetes service account")

// Kube reads Jobs, CronJobs and Pods from the API server with the pod's
// service account.
type Kube struct {
	Base  string
	Token func() (string, error)
	HTTP  *http.Client
}

// InCluster is a Kube for the pod's own API server.
func InCluster() (*Kube, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, ErrNotInCluster
	}
	ca, err := os.ReadFile(saDir + "ca.crt")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotInCluster, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("%w: bad ca.crt", ErrNotInCluster)
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	return &Kube{
		Base: "https://" + net.JoinHostPort(host, port),
		Token: func() (string, error) {
			b, err := os.ReadFile(saDir + "token")
			return strings.TrimSpace(string(b)), err
		},
		HTTP: &http.Client{Transport: tr, Timeout: 20 * time.Second},
	}, nil
}

func (k *Kube) get(ctx context.Context, path string, q url.Values, v any) error {
	u := k.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if k.Token != nil {
		tok, err := k.Token()
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", "application/json")
	res, err := k.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("GET %s: %s: %s", path, res.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(res.Body).Decode(v)
}

// KubeOwner is an owner reference.
type KubeOwner struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// KubeCondition is a Job condition.
type KubeCondition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}

// KubeTerminated is how a container ended.
type KubeTerminated struct {
	Reason   string `json:"reason"`
	ExitCode int    `json:"exitCode"`
}

// KubeContainerStatus is a container's state now and before its last
// restart.
type KubeContainerStatus struct {
	State struct {
		Terminated *KubeTerminated `json:"terminated"`
	} `json:"state"`
	LastState struct {
		Terminated *KubeTerminated `json:"terminated"`
	} `json:"lastState"`
}

// KubeJob is the part of a batch/v1 Job read here.
type KubeJob struct {
	Metadata struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		Labels            map[string]string `json:"labels"`
		CreationTimestamp time.Time         `json:"creationTimestamp"`
		OwnerReferences   []KubeOwner       `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		Completions *int `json:"completions"`
		Parallelism *int `json:"parallelism"`
	} `json:"spec"`
	Status struct {
		Active         int             `json:"active"`
		Succeeded      int             `json:"succeeded"`
		Failed         int             `json:"failed"`
		StartTime      *time.Time      `json:"startTime"`
		CompletionTime *time.Time      `json:"completionTime"`
		Conditions     []KubeCondition `json:"conditions"`
	} `json:"status"`
}

// CronJob names the CronJob that made the job, if one did.
func (j KubeJob) CronJob() string {
	for _, o := range j.Metadata.OwnerReferences {
		if o.Kind == "CronJob" {
			return o.Name
		}
	}
	return ""
}

// Finished says whether the job is over, and when and how.
func (j KubeJob) Finished() (at time.Time, failed bool, reason string, ok bool) {
	for _, c := range j.Status.Conditions {
		if c.Status != "True" {
			continue
		}
		switch c.Type {
		case "Complete", "SuccessCriteriaMet":
			at := c.LastTransitionTime
			if j.Status.CompletionTime != nil {
				at = *j.Status.CompletionTime
			}
			return at, false, "", true
		case "Failed", "FailureTarget":
			r := c.Reason
			if c.Message != "" {
				r = c.Message
			}
			return c.LastTransitionTime, true, r, true
		}
	}
	return time.Time{}, false, "", false
}

// KubeCronJob is the part of a batch/v1 CronJob read here.
type KubeCronJob struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Schedule string  `json:"schedule"`
		TimeZone *string `json:"timeZone"`
		Suspend  *bool   `json:"suspend"`
	} `json:"spec"`
	Status struct {
		LastScheduleTime   *time.Time `json:"lastScheduleTime"`
		LastSuccessfulTime *time.Time `json:"lastSuccessfulTime"`
	} `json:"status"`
}

// KubePod is the part of a Pod read here.
type KubePod struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Status struct {
		Phase             string                `json:"phase"`
		ContainerStatuses []KubeContainerStatus `json:"containerStatuses"`
	} `json:"status"`
}

// Jobs lists the namespace's Jobs.
func (k *Kube) Jobs(ctx context.Context, ns string) ([]KubeJob, error) {
	var l struct {
		Items []KubeJob `json:"items"`
	}
	err := k.get(ctx, "/apis/batch/v1/namespaces/"+url.PathEscape(ns)+"/jobs", nil, &l)
	return l.Items, err
}

// CronJobs lists the namespace's CronJobs.
func (k *Kube) CronJobs(ctx context.Context, ns string) ([]KubeCronJob, error) {
	var l struct {
		Items []KubeCronJob `json:"items"`
	}
	err := k.get(ctx, "/apis/batch/v1/namespaces/"+url.PathEscape(ns)+"/cronjobs", nil, &l)
	return l.Items, err
}

// JobPods lists the pods of the named Job.
func (k *Kube) JobPods(ctx context.Context, ns, job string) ([]KubePod, error) {
	var l struct {
		Items []KubePod `json:"items"`
	}
	q := url.Values{"labelSelector": {"batch.kubernetes.io/job-name=" + job}}
	err := k.get(ctx, "/api/v1/namespaces/"+url.PathEscape(ns)+"/pods", q, &l)
	return l.Items, err
}

// OOMKilled counts the pods whose container ran out of memory, now or
// before a restart.
func OOMKilled(pods []KubePod) int {
	n := 0
	for _, p := range pods {
		for _, c := range p.Status.ContainerStatuses {
			if (c.State.Terminated != nil && c.State.Terminated.Reason == "OOMKilled") ||
				(c.LastState.Terminated != nil && c.LastState.Terminated.Reason == "OOMKilled") {
				n++
			}
		}
	}
	return n
}
