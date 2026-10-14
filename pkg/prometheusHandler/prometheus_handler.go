package prometheusHandler

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type prometheusClient struct {
	HTTPoutput string
	Label      map[string]string
}

type prometheusHistInfo struct {
	histoMap         map[string]string
	totalObservation int
}

// formatValue renders a metric value; bools become 1/0 since Prometheus
// samples must be numeric.
func formatValue(v reflect.Value) string {
	if v.Kind() == reflect.Bool {
		if v.Bool() {
			return "1"
		}
		return "0"
	}
	return fmt.Sprintf("%v", v)
}

func parseCounter(count reflect.Value) string {
	return formatValue(count)
}

func parseHistogram(histogram reflect.Value, histogramType reflect.Type) prometheusHistInfo {
	prometheusHistInfo := prometheusHistInfo{
		histoMap:         make(map[string]string),
		totalObservation: 0,
	}
	for i := 0; i < histogram.NumField(); i++ {
		bucketBound := histogramType.Field(i)
		bucketValue := histogram.Field(i)
		//Discard intervals with 0 value
		if int(bucketValue.Int()) != 0 {
			count := int(bucketValue.Int())
			upperBound := strings.Split(bucketBound.Tag.Get("json"), ",")[0]
			prometheusHistInfo.histoMap[upperBound] = fmt.Sprintf("%v", bucketValue)
			prometheusHistInfo.totalObservation += count
		}
	}
	return prometheusHistInfo
}

func parseGauge(gauge reflect.Value) string {
	return formatValue(gauge)
}

func makePromUntype(metric string, label string, value reflect.Value) string {
	strip := fmt.Sprintf("%v", value)
	output := fmt.Sprintf(`
# UNtype output
%s{%s} %s`, metric, label, strip)
	return output
}

func makePromCounter(metric string, label string, count string) string {
	output := fmt.Sprintf(`
# HELP %s output
# TYPE %s counter
`, metric, metric)
	entry := fmt.Sprintf(`%s%s{%s} %s`, output, metric, label, count)
	return entry + "\n"
}

func makePromHistogram(metric string, label string, histogramInfo prometheusHistInfo) string {
	output := fmt.Sprintf(`
# HELP %s histogram output
# TYPE %s histogram`, metric, metric)

	//Seperate the histogram info
	histogram := histogramInfo.histoMap
	totalObservations := histogramInfo.totalObservation

	//Sort bound
	var bounds []int
	for boundStr, _ := range histogram {
		bound, _ := strconv.Atoi(boundStr)
		bounds = append(bounds, bound)
	}
	sort.Ints(bounds)

	for _, bound := range bounds {
		entry := fmt.Sprintf(`%s_bucket{%s,ge="%d"} %s`, metric, label, bound, histogram[strconv.Itoa(bound)])
		output += "\n" + entry
	}
	entry := fmt.Sprintf("%s_count %s", metric, strconv.Itoa(totalObservations))
	output += "\n\n" + entry + "\n"
	return output
}

func makePromGauge(metric string, label string, value string) string {
	output := fmt.Sprintf(`
# HELP %s gauge output
# TYPE %s gauge
`, metric, metric)
	entry := fmt.Sprintf(`%s%s{%s} %s`, output, metric, label, value)
	return entry + "\n"
}

func makeLabelString(label map[string]string) string {
	var output []string
	for key, value := range label {
		output = append(output, fmt.Sprintf(`%s="%s"`, key, value))
	}
	return strings.Join(output, ",")
}

func GenericPromDataParser(structure interface{}, labels map[string]string) string {
	var op string

	//Reflect the struct
	typeExtract := reflect.TypeOf(structure)
	valueExtract := reflect.ValueOf(structure)
	labelString := makeLabelString(labels)

	//Iterating over fields and compress their values
	for i := 0; i < typeExtract.NumField(); i++ {
		fieldType := typeExtract.Field(i)
		fieldValue := valueExtract.Field(i)

		promType := fieldType.Tag.Get("type")
		promMetric := fieldType.Tag.Get("metric")
		switch promType {
		case "histogram":
			histogramInfo := parseHistogram(fieldValue, fieldType.Type)
			op += makePromHistogram(promMetric, labelString, histogramInfo)
		case "counter":
			count := parseCounter(fieldValue)
			op += makePromCounter(promMetric, labelString, count)
		case "gauge":
			value := parseGauge(fieldValue)
			op += makePromGauge(promMetric, labelString, value)
		case "untype":
			op += makePromUntype(promMetric, labelString, fieldValue)
		}
	}
	return op
}
