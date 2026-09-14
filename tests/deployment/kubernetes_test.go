package deployment_test

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

type object map[string]any

func TestPhase5TopologyAndAvailability(t *testing.T) {
	objects := loadObjects(t)
	requireObject(t, objects, "Namespace", "coding-judge")

	api := requireObject(t, objects, "Deployment", "judge-api")
	worker := requireObject(t, objects, "Deployment", "judge-worker")
	if integer(t, at(t, api, "spec", "replicas")) < 3 {
		t.Fatal("API deployment must start at three or more replicas")
	}
	if integer(t, at(t, worker, "spec", "replicas")) < 3 {
		t.Fatal("worker deployment must start at three or more replicas")
	}

	assertProbe(t, api, "api", "/health", "/ready", 8080)
	assertProbe(t, worker, "worker", "/health", "/ready", 8081)
	assertResources(t, api, "api")
	assertResources(t, worker, "worker")

	service := requireObject(t, objects, "Service", "judge-api")
	port := firstMap(t, at(t, service, "spec", "ports"))
	if integer(t, port["port"]) != 80 || integer(t, port["targetPort"]) != 8080 {
		t.Fatal("API service must route port 80 to the API's port 8080")
	}
	ingress := requireObject(t, objects, "Ingress", "judge-api")
	backend := at(t, firstMap(t, at(t, firstMap(t, at(t, ingress, "spec", "rules")), "http", "paths")), "backend", "service")
	if stringValue(t, at(t, backend, "name")) != "judge-api" {
		t.Fatal("ingress must route to the judge-api service")
	}

	hpa := requireObject(t, objects, "HorizontalPodAutoscaler", "judge-worker")
	if stringValue(t, at(t, hpa, "spec", "scaleTargetRef", "name")) != "judge-worker" {
		t.Fatal("worker HPA must target the judge-worker deployment")
	}
	if integer(t, at(t, hpa, "spec", "minReplicas")) < 3 || integer(t, at(t, hpa, "spec", "maxReplicas")) < 6 {
		t.Fatal("worker HPA must preserve three workers and allow meaningful scale-out")
	}
	metric := firstMap(t, at(t, hpa, "spec", "metrics"))
	if stringValue(t, at(t, metric, "resource", "name")) != "cpu" || integer(t, at(t, metric, "resource", "target", "averageUtilization")) <= 0 {
		t.Fatal("worker HPA must contain a valid CPU utilization target")
	}
	pdb := requireObject(t, objects, "PodDisruptionBudget", "judge-api")
	if integer(t, at(t, pdb, "spec", "minAvailable")) < 2 {
		t.Fatal("API disruption budget must keep at least two replicas available")
	}
	if findObject(objects, "PodDisruptionBudget", "judge-worker") != nil {
		t.Fatal("workers must not have a disruption budget that can block node maintenance")
	}
}

func TestPhase5WorkerIsolationAndNetworkPolicy(t *testing.T) {
	objects := loadObjects(t)
	api := requireObject(t, objects, "Deployment", "judge-api")
	worker := requireObject(t, objects, "Deployment", "judge-worker")

	for _, name := range []string{"judge-api", "judge-worker"} {
		account := requireObject(t, objects, "ServiceAccount", name)
		if value, ok := account["automountServiceAccountToken"].(bool); !ok || value {
			t.Fatalf("service account %s must disable token mounting", name)
		}
	}
	assertContainerSecurity(t, api, "api")
	assertContainerSecurity(t, worker, "worker")
	assertDatabaseSecretRef(t, api, "api")
	assertDatabaseSecretRef(t, worker, "worker")

	podSpec := mapValue(t, at(t, worker, "spec", "template", "spec"))
	if podSpec["hostPID"] != true {
		t.Fatal("worker must see host process cgroups to attribute sandbox memory and OOM events")
	}
	if stringValue(t, at(t, podSpec, "nodeSelector", "workload")) != "judge" {
		t.Fatal("worker must select the dedicated judge node pool")
	}
	if !hasToleration(t, podSpec, "dedicated", "judge", "NoSchedule") {
		t.Fatal("worker must tolerate only the dedicated judge-node taint")
	}
	assertHostPath(t, podSpec, "docker-socket", "/var/run/docker.sock", "Socket")
	assertHostPath(t, podSpec, "host-cgroup", "/sys/fs/cgroup", "Directory")
	workerContainer := namedMap(t, at(t, podSpec, "containers"), "name", "worker")
	assertReadOnlyMount(t, workerContainer, "host-cgroup")

	deny := requireObject(t, objects, "NetworkPolicy", "default-deny")
	if len(mapValue(t, at(t, deny, "spec", "podSelector"))) != 0 {
		t.Fatal("default-deny policy must select every pod in the namespace")
	}
	assertStringSet(t, at(t, deny, "spec", "policyTypes"), "Ingress", "Egress")
	if len(sliceValue(t, at(t, deny, "spec", "ingress"))) != 0 || len(sliceValue(t, at(t, deny, "spec", "egress"))) != 0 {
		t.Fatal("default-deny policy must not contain allow rules")
	}
	apiPolicy := requireObject(t, objects, "NetworkPolicy", "allow-api")
	assertPolicyPorts(t, at(t, apiPolicy, "spec", "ingress"), 8080)
	assertPolicyPorts(t, at(t, apiPolicy, "spec", "egress"), 53, 5432, 6379)
	workerPolicy := requireObject(t, objects, "NetworkPolicy", "allow-worker-control-plane")
	assertPolicyPorts(t, at(t, workerPolicy, "spec", "egress"), 53, 5432, 6379)
	if findObject(objects, "Secret", "judge-runtime-secrets") != nil {
		t.Fatal("deployable manifests must not contain repository-managed credentials")
	}
}

func TestApplicationImagesAreBuildableAndNonRoot(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{"AS api", "AS worker", "ENTRYPOINT [\"/api\"]", "ENTRYPOINT [\"/worker\"]", "USER 65532:65532"} {
		if !strings.Contains(text, required) {
			t.Fatalf("application Dockerfile is missing %q", required)
		}
	}
	if strings.Count(text, "@sha256:") != 3 {
		t.Fatal("every application-image base must be pinned by digest")
	}
}

func loadObjects(t *testing.T) []object {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "deploy", "k8s", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no Kubernetes manifests found")
	}
	var objects []object
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		decoder := yaml.NewDecoder(file)
		for {
			var value object
			err = decoder.Decode(&value)
			if err == io.EOF {
				break
			}
			if err != nil {
				file.Close()
				t.Fatalf("decode %s: %v", path, err)
			}
			if len(value) != 0 {
				objects = append(objects, value)
			}
		}
		file.Close()
	}
	return objects
}

func requireObject(t *testing.T, objects []object, kind, name string) object {
	t.Helper()
	value := findObject(objects, kind, name)
	if value == nil {
		t.Fatalf("missing %s %s", kind, name)
	}
	return value
}

func findObject(objects []object, kind, name string) object {
	for _, value := range objects {
		metadata, _ := value["metadata"].(map[string]any)
		if value["kind"] == kind && metadata["name"] == name {
			return value
		}
	}
	return nil
}

func at(t *testing.T, value any, path ...string) any {
	t.Helper()
	current := value
	for _, key := range path {
		current = mapValue(t, current)[key]
		if current == nil {
			t.Fatalf("missing field %s", strings.Join(path, "."))
		}
	}
	return current
}

func mapValue(t *testing.T, value any) map[string]any {
	t.Helper()
	switch result := value.(type) {
	case map[string]any:
		return result
	case object:
		return map[string]any(result)
	default:
		t.Fatalf("expected map, got %T", value)
		return nil
	}
}

func sliceValue(t *testing.T, value any) []any {
	t.Helper()
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("expected slice, got %T", value)
	}
	return result
}

func firstMap(t *testing.T, value any) map[string]any {
	t.Helper()
	values := sliceValue(t, value)
	if len(values) == 0 {
		t.Fatal("expected a non-empty list")
	}
	return mapValue(t, values[0])
}

func namedMap(t *testing.T, value any, key, name string) map[string]any {
	t.Helper()
	for _, item := range sliceValue(t, value) {
		candidate := mapValue(t, item)
		if candidate[key] == name {
			return candidate
		}
	}
	t.Fatalf("missing list entry %s=%s", key, name)
	return nil
}

func integer(t *testing.T, value any) int64 {
	t.Helper()
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(reflected.Uint())
	case reflect.Float32, reflect.Float64:
		return int64(reflected.Float())
	default:
		t.Fatalf("expected number, got %T", value)
		return 0
	}
}

func stringValue(t *testing.T, value any) string {
	t.Helper()
	result, ok := value.(string)
	if !ok {
		t.Fatalf("expected string, got %T", value)
	}
	return result
}

func assertProbe(t *testing.T, deployment object, container, livePath, readyPath string, port int64) {
	t.Helper()
	entry := namedMap(t, at(t, deployment, "spec", "template", "spec", "containers"), "name", container)
	if stringValue(t, at(t, entry, "livenessProbe", "httpGet", "path")) != livePath || integer(t, at(t, entry, "livenessProbe", "httpGet", "port")) != port {
		t.Fatalf("%s has an invalid liveness probe", container)
	}
	if stringValue(t, at(t, entry, "readinessProbe", "httpGet", "path")) != readyPath || integer(t, at(t, entry, "readinessProbe", "httpGet", "port")) != port {
		t.Fatalf("%s has an invalid readiness probe", container)
	}
}

func assertResources(t *testing.T, deployment object, container string) {
	t.Helper()
	entry := namedMap(t, at(t, deployment, "spec", "template", "spec", "containers"), "name", container)
	for _, class := range []string{"requests", "limits"} {
		for _, resource := range []string{"cpu", "memory"} {
			if stringValue(t, at(t, entry, "resources", class, resource)) == "" {
				t.Fatalf("%s lacks a %s %s", container, class, resource)
			}
		}
	}
}

func assertContainerSecurity(t *testing.T, deployment object, container string) {
	t.Helper()
	podSpec := mapValue(t, at(t, deployment, "spec", "template", "spec"))
	if podSpec["automountServiceAccountToken"] != false {
		t.Fatalf("%s pod must disable service-account token mounting", container)
	}
	entry := namedMap(t, podSpec["containers"], "name", container)
	security := mapValue(t, at(t, entry, "securityContext"))
	if security["allowPrivilegeEscalation"] != false || security["readOnlyRootFilesystem"] != true || security["runAsNonRoot"] != true {
		t.Fatalf("%s container security context is incomplete", container)
	}
	assertStringSet(t, at(t, security, "capabilities", "drop"), "ALL")
	if stringValue(t, entry["image"]) == "" || strings.HasSuffix(stringValue(t, entry["image"]), ":latest") {
		t.Fatalf("%s must use an explicitly versioned image", container)
	}
}

func assertDatabaseSecretRef(t *testing.T, deployment object, container string) {
	t.Helper()
	entry := namedMap(t, at(t, deployment, "spec", "template", "spec", "containers"), "name", container)
	env := namedMap(t, at(t, entry, "env"), "name", "DATABASE_URL")
	if _, exists := env["value"]; exists {
		t.Fatalf("%s must not contain a literal database URL", container)
	}
	if stringValue(t, at(t, env, "valueFrom", "secretKeyRef", "name")) != "judge-runtime-secrets" {
		t.Fatalf("%s must read DATABASE_URL from judge-runtime-secrets", container)
	}
}

func hasToleration(t *testing.T, podSpec map[string]any, key, value, effect string) bool {
	t.Helper()
	for _, item := range sliceValue(t, podSpec["tolerations"]) {
		entry := mapValue(t, item)
		if entry["key"] == key && entry["value"] == value && entry["effect"] == effect {
			return true
		}
	}
	return false
}

func assertHostPath(t *testing.T, podSpec map[string]any, name, path, kind string) {
	t.Helper()
	volume := namedMap(t, podSpec["volumes"], "name", name)
	if stringValue(t, at(t, volume, "hostPath", "path")) != path || stringValue(t, at(t, volume, "hostPath", "type")) != kind {
		t.Fatalf("volume %s must mount %s as %s", name, path, kind)
	}
}

func assertReadOnlyMount(t *testing.T, container map[string]any, name string) {
	t.Helper()
	mount := namedMap(t, container["volumeMounts"], "name", name)
	if mount["readOnly"] != true {
		t.Fatalf("mount %s must be read-only", name)
	}
}

func assertStringSet(t *testing.T, value any, expected ...string) {
	t.Helper()
	actual := make(map[string]bool)
	for _, item := range sliceValue(t, value) {
		actual[stringValue(t, item)] = true
	}
	for _, item := range expected {
		if !actual[item] {
			t.Fatalf("missing %q in %v", item, actual)
		}
	}
}

func assertPolicyPorts(t *testing.T, rules any, expected ...int64) {
	t.Helper()
	actual := make(map[int64]bool)
	for _, item := range sliceValue(t, rules) {
		rule := mapValue(t, item)
		for _, portValue := range sliceValue(t, rule["ports"]) {
			actual[integer(t, mapValue(t, portValue)["port"])] = true
		}
	}
	for _, port := range expected {
		if !actual[port] {
			t.Fatalf("network policy is missing port %d", port)
		}
	}
}
