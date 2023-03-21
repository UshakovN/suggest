package main

func PrepareCheckMap(values []string) map[string]bool {
  valuesMap := map[string]bool{}
  for _, value := range values {
    if value != "" {
      valuesMap[LowerNormalizeString(value)] = true
    }
  }
  return valuesMap
}
