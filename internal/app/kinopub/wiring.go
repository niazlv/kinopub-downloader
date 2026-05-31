package kinopub

import (
	"fmt"
	"reflect"

	"kinopub_downloader/internal/domain"
)

// validateDependencies checks that no field in the Dependencies struct is nil.
// Returns ErrMissingDependency wrapping the field name if any dependency is absent.
func validateDependencies(deps Dependencies) error {
	v := reflect.ValueOf(deps)
	t := v.Type()

	for i := range t.NumField() {
		field := v.Field(i)
		if field.Kind() == reflect.Interface && field.IsNil() {
			return fmt.Errorf("%w: %s", domain.ErrMissingDependency, t.Field(i).Name)
		}
	}
	return nil
}
