# time-to-k8s

Benchmark the time to go from 0 to a successful Kubernetes deployment, generating a CSV file for further analysis. Test cases can be built using any command-line to procure a Kubernetes cluster, for example, using GKE, minikube, or even kubeadm directly.

## Example: Kubernetes versions

![Kubernetes versions graph](https://github.com/tstromberg/time-to-k8s/blob/master/images/versions.png)

This graph was generated from:

`go run . --config kubernetes-versions.yaml --iterations 5`

In a previous generation of this graph, we were able to detect a performance regression with the v1.19 Kubelet inside of minikube.

## Example: Local Kubernetes distributions

![Local Kubernetes graph](https://github.com/tstromberg/time-to-k8s/blob/master/images/local.png)
This graph was generated from:

`go run . --config local-kubernetes.yaml`
