package backend

import (
	"reflect"
	"regexp"
	"testing"
)

func TestAssociationIDUsesFrozen128BitHash(t *testing.T) {
	id := newAssociationID("app/db", "fake", "default", "prod/app/db", syncGranularitySecretPath)
	const want = "assoc-bf93063f318e089f9bced8488e5b70e2"
	if id != want {
		t.Fatalf("association ID = %q, want frozen 128-bit hash ID %q", id, want)
	}
	if !regexp.MustCompile("^" + associationIDPattern + "$").MatchString(id) {
		t.Fatalf("association ID %q does not match path pattern %q", id, associationIDPattern)
	}
}

func TestStorageRecordJSONTags(t *testing.T) {
	for _, testCase := range []struct {
		name string
		typ  reflect.Type
		want []string
	}{
		{
			name: "storage schema",
			typ:  reflect.TypeOf(storageSchemaRecord{}),
			want: []string{
				"Version:version",
				"MinCompatibleVersion:min_compatible_version",
				"CreatedTime:created_time",
				"UpdatedTime:updated_time",
			},
		},
		{
			name: "plugin instance",
			typ:  reflect.TypeOf(pluginInstanceRecord{}),
			want: []string{
				"ID:id",
				"CreatedTime:created_time",
			},
		},
		{
			name: "restore epoch",
			typ:  reflect.TypeOf(restoreEpochRecord{}),
			want: []string{
				"Epoch:epoch",
				"CreatedTime:created_time",
				"UpdatedTime:updated_time",
			},
		},
		{
			name: "source version",
			typ:  reflect.TypeOf(versionRecord{}),
			want: []string{
				"Version:version",
				"CreatedTime:created_time",
				"Data:data",
				"DeletionTime:deletion_time",
				"Destroyed:destroyed",
			},
		},
		{
			name: "version metadata",
			typ:  reflect.TypeOf(versionMetadata{}),
			want: []string{
				"CreatedTime:created_time",
				"DeletionTime:deletion_time",
				"Destroyed:destroyed",
			},
		},
		{
			name: "source metadata",
			typ:  reflect.TypeOf(metadataRecord{}),
			want: []string{
				"Generation:generation",
				"CurrentVersion:current_version",
				"OldestVersion:oldest_version",
				"MaxVersions:max_versions",
				"CASRequired:cas_required",
				"DeleteVersionAfter:delete_version_after",
				"SourceSyncEnabled:source_sync_enabled",
				"CustomMetadata:custom_metadata",
				"Versions:versions",
				"UpdatedTime:updated_time",
			},
		},
		{
			name: "destination",
			typ:  reflect.TypeOf(destinationRecord{}),
			want: []string{
				"Type:type",
				"Name:name",
				"Description:description",
				"Disabled:disabled",
				"Config:config",
				"SensitiveConfigVersion:sensitive_config_version,omitempty",
				"AllowedSourcePathPrefixes:allowed_source_path_prefixes,omitempty",
				"AllowedResolvedNamePrefixes:allowed_resolved_name_prefixes,omitempty",
				"CreatedTime:created_time",
				"UpdatedTime:updated_time",
			},
		},
		{
			name: "destination sensitive config",
			typ:  reflect.TypeOf(destinationSensitiveRecord{}),
			want: []string{
				"Type:type",
				"Name:name",
				"Config:config",
				"CreatedTime:created_time",
				"UpdatedTime:updated_time",
			},
		},
		{
			name: "association",
			typ:  reflect.TypeOf(associationRecord{}),
			want: []string{
				"ID:id",
				"Path:path",
				"DestinationType:destination_type",
				"DestinationName:destination_name",
				"DestinationRef:destination_ref",
				"NameTemplate:name_template",
				"ResolvedName:resolved_name",
				"ReservationNames:reservation_names,omitempty",
				"Granularity:granularity",
				"Format:format",
				"DataMapping:data_mapping,omitempty",
				"DataKeyTemplate:data_key_template,omitempty",
				"ProviderConfig:provider_config,omitempty",
				"ProviderIdentity:provider_identity,omitempty",
				"DeleteMode:delete_mode",
				"Enabled:enabled",
				"CreatedTime:created_time",
				"UpdatedTime:updated_time",
			},
		},
		{
			name: "enqueue intent",
			typ:  reflect.TypeOf(enqueueIntentRecord{}),
			want: []string{
				"Path:path",
				"Generation:generation",
				"Version:version",
				"Operations:operations",
				"CancelOperationIDs:cancel_operation_ids",
				"CreatedTime:created_time",
				"UpdatedTime:updated_time",
			},
		},
		{
			name: "enqueue intent operation",
			typ:  reflect.TypeOf(enqueueIntentOperation{}),
			want: []string{
				"ID:id",
				"Type:type",
				"AssociationID:association_id",
				"ObjectID:object_id",
				"DestinationRef:destination_ref",
				"NotBefore:not_before",
				"IdempotencyKey:idempotency_key",
				"Trigger:trigger,omitempty",
			},
		},
		{
			name: "outbox",
			typ:  reflect.TypeOf(outboxRecord{}),
			want: []string{
				"ID:id",
				"Type:type",
				"Path:path",
				"Version:version",
				"AssociationID:association_id",
				"ObjectID:object_id",
				"DestinationRef:destination_ref",
				"State:state",
				"Attempts:attempts",
				"NotBefore:not_before",
				"CreatedTime:created_time",
				"UpdatedTime:updated_time",
				"IdempotencyKey:idempotency_key",
				"Trigger:trigger,omitempty",
				"ClaimOwner:claim_owner,omitempty",
				"ClaimExpiresTime:claim_expires_time,omitempty",
				"ClaimAttempt:claim_attempt,omitempty",
			},
		},
		{
			name: "status",
			typ:  reflect.TypeOf(statusRecord{}),
			want: []string{
				"Path:path",
				"Version:version",
				"AssociationID:association_id",
				"ObjectID:object_id",
				"DestinationRef:destination_ref",
				"ResolvedName:resolved_name",
				"State:state",
				"PayloadSHA256:payload_sha256",
				"RemoteVersion:remote_version",
				"Verification:verification,omitempty",
				"LastOperationID:last_operation_id",
				"LastSuccessTime:last_success_time",
				"LastReconcileTime:last_reconcile_time,omitempty",
				"LastDriftDetectedTime:last_drift_detected_time,omitempty",
				"LastRepairTime:last_repair_time,omitempty",
				"RepairCount:repair_count,omitempty",
				"LastErrorClass:last_error_class",
				"LastError:last_error",
				"UpdatedTime:updated_time",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := storageRecordJSONTags(testCase.typ)
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("storage record JSON tags = %#v, want %#v", got, testCase.want)
			}
		})
	}
}

func storageRecordJSONTags(typ reflect.Type) []string {
	tags := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tags = append(tags, field.Name+":"+field.Tag.Get("json"))
	}
	return tags
}
