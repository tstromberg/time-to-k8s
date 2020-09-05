# time-to-k8s

Benchmark the time to go from 0 to a successful Kubernetes deployment, generating a CSV file for further analysis. Test cases can be built using any command-line to procure a Kubernetes cluster, for example, using GKE, minikube, or even kubeadm directly.

For example:

`go run . --config local-kubernetes.yaml`

Runs 10 test iterations of minikube, kind, and k3d, outputting a CSV file which allows the following chart to be generated:

[local-compared.png]

This is also useful for proving performance regressions:

[versions-compared.png]
