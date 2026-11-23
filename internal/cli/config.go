package cli

import (
	"errors"

	"k8s.io/apimachinery/pkg/util/validation"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func loadConfig(options configOptions) (*rest.Config, string, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = options.kubeconfig
	raw, err := rules.Load()
	if err != nil {
		return nil, options.context, "", errors.New("Could not load kubeconfig. Check the file path and configuration syntax.")
	}
	contextName := options.context
	if contextName == "" {
		contextName = raw.CurrentContext
	}
	if contextName == "" || raw.Contexts[contextName] == nil {
		return nil, contextName, "", errors.New("The requested kubeconfig context is unavailable. Select an existing context with --context.")
	}
	client := clientcmd.NewNonInteractiveClientConfig(*raw, contextName, &clientcmd.ConfigOverrides{}, rules)
	config, err := client.ClientConfig()
	if err != nil {
		return nil, contextName, "", errors.New("Could not configure Kubernetes authentication. Check the cluster and user in the selected context.")
	}
	namespace := raw.Contexts[contextName].Namespace
	if namespace == "" {
		namespace = "default"
	}
	return config, contextName, namespace, nil
}

func validNamespace(value string) bool {
	return len(validation.IsDNS1123Label(value)) == 0
}
