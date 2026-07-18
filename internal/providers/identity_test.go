package providers

import "testing"

func TestRequestIdentityCompleteRequiresMountAndRestoreIdentity(t *testing.T) {
	complete := RequestIdentity{
		AssociationID: "assoc-1",
		SourcePath:    "app/db",
		ObjectID:      "secret-path",
		MountUUID:     "00000000-0000-4000-8000-000000000001",
		RestoreEpoch:  "epoch-test",
	}
	if !complete.Complete() {
		t.Fatal("complete ownership identity was rejected")
	}

	for name, mutate := range map[string]func(*RequestIdentity){
		"association":   func(identity *RequestIdentity) { identity.AssociationID = "" },
		"source path":   func(identity *RequestIdentity) { identity.SourcePath = "" },
		"object":        func(identity *RequestIdentity) { identity.ObjectID = "" },
		"mount UUID":    func(identity *RequestIdentity) { identity.MountUUID = "" },
		"restore epoch": func(identity *RequestIdentity) { identity.RestoreEpoch = "" },
	} {
		t.Run(name, func(t *testing.T) {
			identity := complete
			mutate(&identity)
			if identity.Complete() {
				t.Fatal("incomplete ownership identity was accepted")
			}
		})
	}
}
