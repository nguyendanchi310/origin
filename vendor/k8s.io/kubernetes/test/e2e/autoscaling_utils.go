/*
Copyright 2015 The Kubernetes Authors.

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

package e2e

import (
	"fmt"
	"strconv"
	"time"

	"k8s.io/kubernetes/pkg/api"
	clientset "k8s.io/kubernetes/pkg/client/clientset_generated/internalclientset"
	"k8s.io/kubernetes/pkg/util/intstr"
	"k8s.io/kubernetes/test/e2e/framework"
	testutils "k8s.io/kubernetes/test/utils"

	. "github.com/onsi/ginkgo"
)

const (
	dynamicConsumptionTimeInSeconds = 30
	staticConsumptionTimeInSeconds  = 3600
	dynamicRequestSizeInMillicores  = 20
	dynamicRequestSizeInMegabytes   = 100
	dynamicRequestSizeCustomMetric  = 10
	port                            = 80
	targetPort                      = 8080
	timeoutRC                       = 120 * time.Second
	startServiceTimeout             = time.Minute
	startServiceInterval            = 5 * time.Second
	resourceConsumerImage           = "gcr.io/google_containers/resource_consumer:beta4"
	resourceConsumerControllerImage = "gcr.io/google_containers/resource_consumer/controller:beta4"
	rcIsNil                         = "ERROR: replicationController = nil"
	deploymentIsNil                 = "ERROR: deployment = nil"
	rsIsNil                         = "ERROR: replicaset = nil"
	invalidKind                     = "ERROR: invalid workload kind for resource consumer"
	customMetricName                = "QPS"
)

/*
ResourceConsumer is a tool for testing. It helps create specified usage of CPU or memory (Warning: memory not supported)
typical use case:
rc.ConsumeCPU(600)
// ... check your assumption here
rc.ConsumeCPU(300)
// ... check your assumption here
*/
type ResourceConsumer struct {
	name                     string
	controllerName           string
	kind                     string
	framework                *framework.Framework
	cpu                      chan int
	mem                      chan int
	customMetric             chan int
	stopCPU                  chan int
	stopMem                  chan int
	stopCustomMetric         chan int
	consumptionTimeInSeconds int
	sleepTime                time.Duration
	requestSizeInMillicores  int
	requestSizeInMegabytes   int
	requestSizeCustomMetric  int
}

func NewDynamicResourceConsumer(name, kind string, replicas, initCPUTotal, initMemoryTotal, initCustomMetric int, cpuLimit, memLimit int64, f *framework.Framework) *ResourceConsumer {
	return newResourceConsumer(name, kind, replicas, initCPUTotal, initMemoryTotal, initCustomMetric, dynamicConsumptionTimeInSeconds,
		dynamicRequestSizeInMillicores, dynamicRequestSizeInMegabytes, dynamicRequestSizeCustomMetric, cpuLimit, memLimit, f)
}

// TODO this still defaults to replication controller
func NewStaticResourceConsumer(name string, replicas, initCPUTotal, initMemoryTotal, initCustomMetric int, cpuLimit, memLimit int64, f *framework.Framework) *ResourceConsumer {
	return newResourceConsumer(name, kindRC, replicas, initCPUTotal, initMemoryTotal, initCustomMetric, staticConsumptionTimeInSeconds,
		initCPUTotal/replicas, initMemoryTotal/replicas, initCustomMetric/replicas, cpuLimit, memLimit, f)
}

/*
NewResourceConsumer creates new ResourceConsumer
initCPUTotal argument is in millicores
initMemoryTotal argument is in megabytes
memLimit argument is in megabytes, memLimit is a maximum amount of memory that can be consumed by a single pod
cpuLimit argument is in millicores, cpuLimit is a maximum amount of cpu that can be consumed by a single pod
*/
func newResourceConsumer(name, kind string, replicas, initCPUTotal, initMemoryTotal, initCustomMetric, consumptionTimeInSeconds, requestSizeInMillicores,
	requestSizeInMegabytes int, requestSizeCustomMetric int, cpuLimit, memLimit int64, f *framework.Framework) *ResourceConsumer {

	runServiceAndWorkloadForResourceConsumer(f.ClientSet, f.Namespace.Name, name, kind, replicas, cpuLimit, memLimit)
	rc := &ResourceConsumer{
		name:                     name,
		controllerName:           name + "-ctrl",
		kind:                     kind,
		framework:                f,
		cpu:                      make(chan int),
		mem:                      make(chan int),
		customMetric:             make(chan int),
		stopCPU:                  make(chan int),
		stopMem:                  make(chan int),
		stopCustomMetric:         make(chan int),
		consumptionTimeInSeconds: consumptionTimeInSeconds,
		sleepTime:                time.Duration(consumptionTimeInSeconds) * time.Second,
		requestSizeInMillicores:  requestSizeInMillicores,
		requestSizeInMegabytes:   requestSizeInMegabytes,
		requestSizeCustomMetric:  requestSizeCustomMetric,
	}

	go rc.makeConsumeCPURequests()
	rc.ConsumeCPU(initCPUTotal)

	go rc.makeConsumeMemRequests()
	rc.ConsumeMem(initMemoryTotal)
	go rc.makeConsumeCustomMetric()
	rc.ConsumeCustomMetric(initCustomMetric)
	return rc
}

// ConsumeCPU consumes given number of CPU
func (rc *ResourceConsumer) ConsumeCPU(millicores int) {
	framework.Logf("RC %s: consume %v millicores in total", rc.name, millicores)
	rc.cpu <- millicores
}

// ConsumeMem consumes given number of Mem
func (rc *ResourceConsumer) ConsumeMem(megabytes int) {
	framework.Logf("RC %s: consume %v MB in total", rc.name, megabytes)
	rc.mem <- megabytes
}

// ConsumeMem consumes given number of custom metric
func (rc *ResourceConsumer) ConsumeCustomMetric(amount int) {
	framework.Logf("RC %s: consume custom metric %v in total", rc.name, amount)
	rc.customMetric <- amount
}

func (rc *ResourceConsumer) makeConsumeCPURequests() {
	defer GinkgoRecover()
	sleepTime := time.Duration(0)
	millicores := 0
	for {
		select {
		case millicores = <-rc.cpu:
			framework.Logf("RC %s: setting consumption to %v millicores in total", rc.name, millicores)
		case <-time.After(sleepTime):
			framework.Logf("RC %s: sending request to consume %d millicores", rc.name, millicores)
			rc.sendConsumeCPURequest(millicores)
			sleepTime = rc.sleepTime
		case <-rc.stopCPU:
			return
		}
	}
}

func (rc *ResourceConsumer) makeConsumeMemRequests() {
	defer GinkgoRecover()
	sleepTime := time.Duration(0)
	megabytes := 0
	for {
		select {
		case megabytes = <-rc.mem:
			framework.Logf("RC %s: setting consumption to %v MB in total", rc.name, megabytes)
		case <-time.After(sleepTime):
			framework.Logf("RC %s: sending request to consume %d MB", rc.name, megabytes)
			rc.sendConsumeMemRequest(megabytes)
			sleepTime = rc.sleepTime
		case <-rc.stopMem:
			return
		}
	}
}

func (rc *ResourceConsumer) makeConsumeCustomMetric() {
	defer GinkgoRecover()
	sleepTime := time.Duration(0)
	delta := 0
	for {
		select {
		case delta := <-rc.customMetric:
			framework.Logf("RC %s: setting bump of metric %s to %d in total", rc.name, customMetricName, delta)
		case <-time.After(sleepTime):
			framework.Logf("RC %s: sending request to consume %d of custom metric %s", rc.name, delta, customMetricName)
			rc.sendConsumeCustomMetric(delta)
			sleepTime = rc.sleepTime
		case <-rc.stopCustomMetric:
			return
		}
	}
}

func (rc *ResourceConsumer) sendConsumeCPURequest(millicores int) {
	proxyRequest, err := framework.GetServicesProxyRequest(rc.framework.ClientSet, rc.framework.ClientSet.Core().RESTClient().Post())
	framework.ExpectNoError(err)
	req := proxyRequest.Namespace(rc.framework.Namespace.Name).
		Name(rc.controllerName).
		Suffix("ConsumeCPU").
		Param("millicores", strconv.Itoa(millicores)).
		Param("durationSec", strconv.Itoa(rc.consumptionTimeInSeconds)).
		Param("requestSizeMillicores", strconv.Itoa(rc.requestSizeInMillicores))
	framework.Logf("URL: %v", *req.URL())
	_, err = req.DoRaw()
	framework.ExpectNoError(err)
}

// sendConsumeMemRequest sends POST request for memory consumption
func (rc *ResourceConsumer) sendConsumeMemRequest(megabytes int) {
	proxyRequest, err := framework.GetServicesProxyRequest(rc.framework.ClientSet, rc.framework.ClientSet.Core().RESTClient().Post())
	framework.ExpectNoError(err)
	req := proxyRequest.Namespace(rc.framework.Namespace.Name).
		Name(rc.controllerName).
		Suffix("ConsumeMem").
		Param("megabytes", strconv.Itoa(megabytes)).
		Param("durationSec", strconv.Itoa(rc.consumptionTimeInSeconds)).
		Param("requestSizeMegabytes", strconv.Itoa(rc.requestSizeInMegabytes))
	framework.Logf("URL: %v", *req.URL())
	_, err = req.DoRaw()
	framework.ExpectNoError(err)
}

// sendConsumeCustomMetric sends POST request for custom metric consumption
func (rc *ResourceConsumer) sendConsumeCustomMetric(delta int) {
	proxyRequest, err := framework.GetServicesProxyRequest(rc.framework.ClientSet, rc.framework.ClientSet.Core().RESTClient().Post())
	framework.ExpectNoError(err)
	req := proxyRequest.Namespace(rc.framework.Namespace.Name).
		Name(rc.controllerName).
		Suffix("BumpMetric").
		Param("metric", customMetricName).
		Param("delta", strconv.Itoa(delta)).
		Param("durationSec", strconv.Itoa(rc.consumptionTimeInSeconds)).
		Param("requestSizeMetrics", strconv.Itoa(rc.requestSizeCustomMetric))
	framework.Logf("URL: %v", *req.URL())
	_, err = req.DoRaw()
	framework.ExpectNoError(err)
}

func (rc *ResourceConsumer) GetReplicas() int {
	switch rc.kind {
	case kindRC:
		replicationController, err := rc.framework.ClientSet.Core().ReplicationControllers(rc.framework.Namespace.Name).Get(rc.name)
		framework.ExpectNoError(err)
		if replicationController == nil {
			framework.Failf(rcIsNil)
		}
		return int(replicationController.Status.Replicas)
	case kindDeployment:
		deployment, err := rc.framework.ClientSet.Extensions().Deployments(rc.framework.Namespace.Name).Get(rc.name)
		framework.ExpectNoError(err)
		if deployment == nil {
			framework.Failf(deploymentIsNil)
		}
		return int(deployment.Status.Replicas)
	case kindReplicaSet:
		rs, err := rc.framework.ClientSet.Extensions().ReplicaSets(rc.framework.Namespace.Name).Get(rc.name)
		framework.ExpectNoError(err)
		if rs == nil {
			framework.Failf(rsIsNil)
		}
		return int(rs.Status.Replicas)
	default:
		framework.Failf(invalidKind)
	}
	return 0
}

func (rc *ResourceConsumer) WaitForReplicas(desiredReplicas int) {
	timeout := 15 * time.Minute
	for start := time.Now(); time.Since(start) < timeout; time.Sleep(20 * time.Second) {
		if desiredReplicas == rc.GetReplicas() {
			framework.Logf("%s: current replicas number is equal to desired replicas number: %d", rc.kind, desiredReplicas)
			return
		} else {
			framework.Logf("%s: current replicas number %d waiting to be %d", rc.kind, rc.GetReplicas(), desiredReplicas)
		}
	}
	framework.Failf("timeout waiting %v for pods size to be %d", timeout, desiredReplicas)
}

func (rc *ResourceConsumer) EnsureDesiredReplicas(desiredReplicas int, timeout time.Duration) {
	for start := time.Now(); time.Since(start) < timeout; time.Sleep(10 * time.Second) {
		actual := rc.GetReplicas()
		if desiredReplicas != actual {
			framework.Failf("Number of replicas has changed: expected %v, got %v", desiredReplicas, actual)
		}
		framework.Logf("Number of replicas is as expected")
	}
	framework.Logf("Number of replicas was stable over %v", timeout)
}

func (rc *ResourceConsumer) CleanUp() {
	By(fmt.Sprintf("Removing consuming RC %s", rc.name))
	close(rc.stopCPU)
	close(rc.stopMem)
	close(rc.stopCustomMetric)
	// Wait some time to ensure all child goroutines are finished.
	time.Sleep(10 * time.Second)
	framework.ExpectNoError(framework.DeleteRCAndPods(rc.framework.ClientSet, rc.framework.Namespace.Name, rc.name))
	framework.ExpectNoError(rc.framework.ClientSet.Core().Services(rc.framework.Namespace.Name).Delete(rc.name, nil))
	framework.ExpectNoError(framework.DeleteRCAndPods(rc.framework.ClientSet, rc.framework.Namespace.Name, rc.controllerName))
	framework.ExpectNoError(rc.framework.ClientSet.Core().Services(rc.framework.Namespace.Name).Delete(rc.controllerName, nil))
}

func runServiceAndWorkloadForResourceConsumer(c clientset.Interface, ns, name, kind string, replicas int, cpuLimitMillis, memLimitMb int64) {
	By(fmt.Sprintf("Running consuming RC %s via %s with %v replicas", name, kind, replicas))
	_, err := c.Core().Services(ns).Create(&api.Service{
		ObjectMeta: api.ObjectMeta{
			Name: name,
		},
		Spec: api.ServiceSpec{
			Ports: []api.ServicePort{{
				Port:       port,
				TargetPort: intstr.FromInt(targetPort),
			}},

			Selector: map[string]string{
				"name": name,
			},
		},
	})
	framework.ExpectNoError(err)

	rcConfig := testutils.RCConfig{
		Client:     c,
		Image:      resourceConsumerImage,
		Name:       name,
		Namespace:  ns,
		Timeout:    timeoutRC,
		Replicas:   replicas,
		CpuRequest: cpuLimitMillis,
		CpuLimit:   cpuLimitMillis,
		MemRequest: memLimitMb * 1024 * 1024, // MemLimit is in bytes
		MemLimit:   memLimitMb * 1024 * 1024,
	}

	switch kind {
	case kindRC:
		framework.ExpectNoError(framework.RunRC(rcConfig))
		break
	case kindDeployment:
		dpConfig := testutils.DeploymentConfig{
			RCConfig: rcConfig,
		}
		framework.ExpectNoError(framework.RunDeployment(dpConfig))
		break
	case kindReplicaSet:
		rsConfig := testutils.ReplicaSetConfig{
			RCConfig: rcConfig,
		}
		By(fmt.Sprintf("creating replicaset %s in namespace %s", rsConfig.Name, rsConfig.Namespace))
		framework.ExpectNoError(framework.RunReplicaSet(rsConfig))
		break
	default:
		framework.Failf(invalidKind)
	}

	By(fmt.Sprintf("Running controller"))
	controllerName := name + "-ctrl"
	_, err = c.Core().Services(ns).Create(&api.Service{
		ObjectMeta: api.ObjectMeta{
			Name: controllerName,
		},
		Spec: api.ServiceSpec{
			Ports: []api.ServicePort{{
				Port:       port,
				TargetPort: intstr.FromInt(targetPort),
			}},

			Selector: map[string]string{
				"name": controllerName,
			},
		},
	})
	framework.ExpectNoError(err)

	dnsClusterFirst := api.DNSClusterFirst
	controllerRcConfig := testutils.RCConfig{
		Client:    c,
		Image:     resourceConsumerControllerImage,
		Name:      controllerName,
		Namespace: ns,
		Timeout:   timeoutRC,
		Replicas:  1,
		Command:   []string{"/controller", "--consumer-service-name=" + name, "--consumer-service-namespace=" + ns, "--consumer-port=80"},
		DNSPolicy: &dnsClusterFirst,
	}
	framework.ExpectNoError(framework.RunRC(controllerRcConfig))

	// Wait for endpoints to propagate for the controller service.
	framework.ExpectNoError(framework.WaitForServiceEndpointsNum(
		c, ns, controllerName, 1, startServiceInterval, startServiceTimeout))
}
