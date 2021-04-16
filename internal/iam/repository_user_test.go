package iam_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/hashicorp/boundary/internal/auth/oidc"
	"github.com/hashicorp/boundary/internal/db"
	dbassert "github.com/hashicorp/boundary/internal/db/assert"
	"github.com/hashicorp/boundary/internal/errors"
	"github.com/hashicorp/boundary/internal/iam"
	"github.com/hashicorp/boundary/internal/kms"
	"github.com/hashicorp/boundary/internal/oplog"
	"github.com/hashicorp/go-uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestRepository_CreateUser(t *testing.T) {
	t.Parallel()
	conn, _ := db.TestSetup(t, "postgres")
	rw := db.New(conn)
	wrapper := db.TestWrapper(t)
	repo := iam.TestRepo(t, conn, wrapper)
	id := testId(t)
	org, _ := iam.TestScopes(t, repo)

	type args struct {
		user *iam.User
		opt  []iam.Option
	}
	tests := []struct {
		name       string
		args       args
		wantDup    bool
		wantErr    bool
		wantErrMsg string
	}{
		{
			name: "valid",
			args: args{
				user: func() *iam.User {
					u, err := iam.NewUser(org.PublicId, iam.WithName("valid"+id), iam.WithDescription(id))
					assert.NoError(t, err)
					return u
				}(),
			},
			wantErr: false,
		},
		{
			name: "bad-scope-id",
			args: args{
				user: func() *iam.User {
					u, err := iam.NewUser(id)
					assert.NoError(t, err)
					return u
				}(),
			},
			wantErr:    true,
			wantErrMsg: "iam.(Repository).create: error getting metadata: iam.(Repository).stdMetadata: unable to get scope: iam.LookupScope: db.LookupWhere: record not found",
		},
		{
			name: "dup-name",
			args: args{
				user: func() *iam.User {
					u, err := iam.NewUser(org.PublicId, iam.WithName("dup-name"+id))
					assert.NoError(t, err)
					return u
				}(),
			},
			wantDup:    true,
			wantErr:    true,
			wantErrMsg: "iam.(Repository).CreateUser: user %s already exists in org %s: integrity violation: error #1002",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			if tt.wantDup {
				dup, err := repo.CreateUser(context.Background(), tt.args.user, tt.args.opt...)
				require.NoError(err)
				require.NotNil(dup)
			}
			u, err := repo.CreateUser(context.Background(), tt.args.user, tt.args.opt...)
			if tt.wantErr {
				require.Error(err)
				assert.Nil(u)
				switch tt.name {
				case "dup-name":
					assert.Contains(err.Error(), fmt.Sprintf(tt.wantErrMsg, "dup-name"+id, org.PublicId))
				default:
					assert.Contains(err.Error(), tt.wantErrMsg)
				}
				return
			}
			require.NoError(err)
			assert.NotNil(u.CreateTime)
			assert.NotNil(u.UpdateTime)

			foundUser, _, err := repo.LookupUser(context.Background(), u.PublicId)
			require.NoError(err)
			assert.True(proto.Equal(foundUser, u))

			err = db.TestVerifyOplog(t, rw, u.PublicId, db.WithOperation(oplog.OpType_OP_TYPE_CREATE), db.WithCreateNotBefore(10*time.Second))
			assert.NoError(err)
		})
	}
}

func TestRepository_UpdateUser(t *testing.T) {
	t.Parallel()
	conn, _ := db.TestSetup(t, "postgres")
	rw := db.New(conn)
	wrapper := db.TestWrapper(t)
	kmsCache := kms.TestKms(t, conn, wrapper)
	repo := iam.TestRepo(t, conn, wrapper)
	id := testId(t)
	org, proj := iam.TestScopes(t, repo)
	databaseWrapper, err := kmsCache.GetWrapper(context.Background(), org.PublicId, kms.KeyPurposeDatabase)
	require.NoError(t, err)

	pubId := func(s string) *string { return &s }

	type args struct {
		name           string
		description    string
		fieldMaskPaths []string
		opt            []iam.Option
		ScopeId        string
		PublicId       *string
	}
	tests := []struct {
		name           string
		newUserOpts    []iam.Option
		args           args
		wantRowsUpdate int
		wantErr        bool
		wantErrMsg     string
		wantIsErr      errors.Code
		wantDup        bool
	}{
		{
			name: "valid",
			args: args{
				name:           "valid" + id,
				fieldMaskPaths: []string{"Name"},
				ScopeId:        org.PublicId,
			},
			wantErr:        false,
			wantRowsUpdate: 1,
		},
		{
			name: "valid-no-op",
			args: args{
				name:           "valid-no-op" + id,
				fieldMaskPaths: []string{"Name"},
				ScopeId:        org.PublicId,
			},
			newUserOpts:    []iam.Option{iam.WithName("valid-no-op" + id)},
			wantErr:        false,
			wantRowsUpdate: 1,
		},
		{
			name: "not-found",
			args: args{
				name:           "not-found" + id,
				fieldMaskPaths: []string{"Name"},
				ScopeId:        org.PublicId,
				PublicId:       func() *string { s := "1"; return &s }(),
			},
			wantErr:        true,
			wantRowsUpdate: 0,
			wantErrMsg:     "iam.(Repository).UpdateUser: db.Update: db.lookupAfterWrite: db.LookupById: record not found, search issue: error #1100",
			wantIsErr:      errors.RecordNotFound,
		},
		{
			name: "null-name",
			args: args{
				name:           "",
				fieldMaskPaths: []string{"Name"},
				ScopeId:        org.PublicId,
			},
			newUserOpts:    []iam.Option{iam.WithName("null-name" + id)},
			wantErr:        false,
			wantRowsUpdate: 1,
		},
		{
			name: "null-description",
			args: args{
				name:           "",
				fieldMaskPaths: []string{"Description"},
				ScopeId:        org.PublicId,
			},
			newUserOpts:    []iam.Option{iam.WithDescription("null-description" + id)},
			wantErr:        false,
			wantRowsUpdate: 1,
		},
		{
			name: "empty-field-mask",
			args: args{
				name:           "valid" + id,
				fieldMaskPaths: []string{},
				ScopeId:        org.PublicId,
			},
			wantErr:        true,
			wantRowsUpdate: 0,
			wantErrMsg:     "iam.(Repository).UpdateUser: empty field mask, parameter violation: error #104",
		},
		{
			name: "nil-fieldmask",
			args: args{
				name:           "valid" + id,
				fieldMaskPaths: nil,
				ScopeId:        org.PublicId,
			},
			wantErr:        true,
			wantRowsUpdate: 0,
			wantErrMsg:     "iam.(Repository).UpdateUser: empty field mask, parameter violation: error #104",
		},
		{
			name: "read-only-fields",
			args: args{
				name:           "valid" + id,
				fieldMaskPaths: []string{"CreateTime"},
				ScopeId:        org.PublicId,
			},
			wantErr:        true,
			wantRowsUpdate: 0,
			wantErrMsg:     "iam.(Repository).UpdateUser: invalid field mask: CreateTime: parameter violation: error #103",
		},
		{
			name: "unknown-fields",
			args: args{
				name:           "valid" + id,
				fieldMaskPaths: []string{"Alice"},
				ScopeId:        org.PublicId,
			},
			wantErr:        true,
			wantRowsUpdate: 0,
			wantErrMsg:     "iam.(Repository).UpdateUser: invalid field mask: Alice: parameter violation: error #103",
		},
		{
			name: "no-public-id",
			args: args{
				name:           "valid" + id,
				fieldMaskPaths: []string{"Name"},
				ScopeId:        org.PublicId,
				PublicId:       pubId(""),
			},
			wantErr:        true,
			wantErrMsg:     "iam.(Repository).UpdateUser: missing public id: parameter violation: error #100",
			wantRowsUpdate: 0,
		},
		{
			name: "proj-scope-id",
			args: args{
				name:           "proj-scope-id" + id,
				fieldMaskPaths: []string{"ScopeId"},
				ScopeId:        proj.PublicId,
			},
			wantErr:    true,
			wantErrMsg: "iam.(Repository).UpdateUser: invalid field mask: ScopeId: parameter violation: error #103",
		},
		{
			name: "empty-scope-id-with-name-mask",
			args: args{
				name:           "empty-scope-id" + id,
				fieldMaskPaths: []string{"Name"},
				ScopeId:        "",
			},
			wantErr:        false,
			wantRowsUpdate: 1,
		},
		{
			name: "dup-name",
			args: args{
				name:           "dup-name" + id,
				fieldMaskPaths: []string{"Name"},
				ScopeId:        org.PublicId,
			},
			wantErr:    true,
			wantDup:    true,
			wantErrMsg: `iam.(Repository).UpdateUser: user %s already exists in org %s: integrity violation: error #1002`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			// we need to clean out any auth methods
			_, err = rw.Exec(context.Background(), "delete from auth_method", nil)
			require.NoError(err)

			if tt.wantDup {
				u := iam.TestUser(t, repo, org.PublicId, tt.newUserOpts...)
				u.Name = tt.args.name
				_, _, _, err := repo.UpdateUser(context.Background(), u, 1, tt.args.fieldMaskPaths, tt.args.opt...)
				require.NoError(err)
			}

			u := iam.TestUser(t, repo, org.PublicId, tt.newUserOpts...)
			acctCount := 3
			accountIds := make([]string, 0, acctCount)
			var wantEmail, wantFullName string
			var authMethod *oidc.AuthMethod
			for i := 0; i < acctCount; i++ {
				authMethod = oidc.TestAuthMethod(t, conn, databaseWrapper, org.PublicId, oidc.ActivePrivateState, fmt.Sprintf("alice-rp-%d", i), "fido",
					oidc.WithIssuer(oidc.TestConvertToUrls(t, "https://alice.com")[0]),
					oidc.WithSigningAlgs(oidc.RS256),
					oidc.WithApiUrl(oidc.TestConvertToUrls(t, "http://localhost")[0]))
				wantEmail, wantFullName = fmt.Sprintf("%s-%d@example.com", tt.name, i), fmt.Sprintf("%s-%d", tt.name, i)
				aa := oidc.TestAccount(t, conn, authMethod, fmt.Sprintf(tt.name, i), oidc.WithFullName(wantFullName), oidc.WithEmail(wantEmail))
				accountIds = append(accountIds, aa.PublicId)
			}
			var s *iam.Scope
			s, err = repo.LookupScope(context.Background(), org.PublicId)
			require.NoError(err)
			iam.TestSetPrimaryAuthMethod(t, repo, s, authMethod.PublicId)

			sort.Strings(accountIds)
			if len(accountIds) > 0 {
				newAccts, err := repo.AddUserAccounts(context.Background(), u.PublicId, u.Version, accountIds)
				require.NoError(err)
				sort.Strings(newAccts)
				require.Equal(accountIds, newAccts)
				u.Version++
			}
			// we need to clean out any oplog entries added because we
			// associated accounts to the test user
			_, err = rw.Exec(context.Background(), "delete from oplog_entry", nil)
			require.NoError(err)

			updateUser := iam.AllocUser()
			updateUser.PublicId = u.PublicId
			if tt.args.PublicId != nil {
				updateUser.PublicId = *tt.args.PublicId
			}
			updateUser.ScopeId = tt.args.ScopeId
			updateUser.Name = tt.args.name
			updateUser.Description = tt.args.description

			var userAfterUpdate *iam.User
			var acctIdsAfterUpdate []string
			var updatedRows int
			var err error
			userAfterUpdate, acctIdsAfterUpdate, updatedRows, err = repo.UpdateUser(context.Background(), &updateUser, u.Version, tt.args.fieldMaskPaths, tt.args.opt...)

			if tt.wantErr {
				require.Error(err)
				assert.True(errors.Match(errors.T(tt.wantIsErr), err))
				assert.Nil(userAfterUpdate)
				assert.Equal(0, updatedRows)
				switch tt.name {
				case "dup-name":
					assert.Equal(fmt.Sprintf(tt.wantErrMsg, "dup-name"+id, org.PublicId), err.Error())
				default:
					assert.Containsf(err.Error(), tt.wantErrMsg, "unexpected error: %s", err.Error())
				}
				err = db.TestVerifyOplog(t, rw, u.PublicId, db.WithOperation(oplog.OpType_OP_TYPE_UPDATE), db.WithCreateNotBefore(1*time.Second))
				require.Error(err)
				assert.Contains(err.Error(), "record not found")
				return
			}
			require.NoError(err)
			assert.Equal(tt.wantRowsUpdate, updatedRows)
			assert.NotEqual(u.UpdateTime, userAfterUpdate.UpdateTime)
			sort.Strings(acctIdsAfterUpdate)
			assert.Equal(accountIds, acctIdsAfterUpdate)
			assert.Equal(wantFullName, userAfterUpdate.FullName)
			assert.Equal(wantEmail, userAfterUpdate.Email)

			foundUser, foundAccountIds, err := repo.LookupUser(context.Background(), u.PublicId)
			require.NoError(err)
			assert.True(proto.Equal(userAfterUpdate, foundUser))
			sort.Strings(foundAccountIds)
			assert.Equal(accountIds, foundAccountIds)

			dbassert := dbassert.New(t, conn.DB())
			if tt.args.name == "" {
				dbassert.IsNull(foundUser, "name")
			}
			if tt.args.description == "" {
				dbassert.IsNull(foundUser, "description")
			}

			err = db.TestVerifyOplog(t, rw, u.PublicId, db.WithOperation(oplog.OpType_OP_TYPE_UPDATE), db.WithCreateNotBefore(10*time.Second))
			assert.NoError(err)
		})
	}
}

func TestRepository_DeleteUser(t *testing.T) {
	t.Parallel()
	conn, _ := db.TestSetup(t, "postgres")
	rw := db.New(conn)
	wrapper := db.TestWrapper(t)
	repo := iam.TestRepo(t, conn, wrapper)
	org, _ := iam.TestScopes(t, repo)

	type args struct {
		user *iam.User
		opt  []iam.Option
	}
	tests := []struct {
		name            string
		args            args
		wantRowsDeleted int
		wantErr         bool
		wantErrMsg      string
	}{
		{
			name: "valid",
			args: args{
				user: iam.TestUser(t, repo, org.PublicId),
			},
			wantRowsDeleted: 1,
			wantErr:         false,
		},
		{
			name: "no-public-id",
			args: args{
				user: func() *iam.User {
					u := iam.AllocUser()
					return &u
				}(),
			},
			wantRowsDeleted: 0,
			wantErr:         true,
			wantErrMsg:      "iam.(Repository).DeleteUser: missing public id: parameter violation: error #100",
		},
		{
			name: "not-found",
			args: args{
				user: func() *iam.User {
					u, err := iam.NewUser(org.PublicId)
					require.NoError(t, err)
					id, err := db.NewPublicId(iam.UserPrefix)
					require.NoError(t, err)
					u.PublicId = id
					return u
				}(),
			},
			wantRowsDeleted: 1,
			wantErr:         true,
			wantErrMsg:      "db.LookupById: record not found, search issue: error #1100",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			deletedRows, err := repo.DeleteUser(context.Background(), tt.args.user.PublicId, tt.args.opt...)
			if tt.wantErr {
				require.Error(err)
				assert.Equal(0, deletedRows)
				assert.Contains(err.Error(), tt.wantErrMsg)
				err = db.TestVerifyOplog(t, rw, tt.args.user.PublicId, db.WithOperation(oplog.OpType_OP_TYPE_DELETE), db.WithCreateNotBefore(10*time.Second))
				require.Error(err)
				assert.Contains(err.Error(), "record not found")
				return
			}
			require.NoError(err)
			assert.Equal(tt.wantRowsDeleted, deletedRows)
			foundUser, _, err := repo.LookupUser(context.Background(), tt.args.user.PublicId)
			require.NoError(err)
			assert.Nil(foundUser)

			err = db.TestVerifyOplog(t, rw, tt.args.user.PublicId, db.WithOperation(oplog.OpType_OP_TYPE_DELETE), db.WithCreateNotBefore(10*time.Second))
			require.NoError(err)
		})
	}
}

func TestRepository_ListUsers(t *testing.T) {
	t.Parallel()
	conn, _ := db.TestSetup(t, "postgres")
	const testLimit = 10
	wrapper := db.TestWrapper(t)
	kmsCache := kms.TestKms(t, conn, wrapper)
	repo := iam.TestRepo(t, conn, wrapper, iam.WithLimit(testLimit))
	org, _ := iam.TestScopes(t, repo)
	databaseWrapper, err := kmsCache.GetWrapper(context.Background(), org.PublicId, kms.KeyPurposeDatabase)
	require.NoError(t, err)
	authMethod := oidc.TestAuthMethod(t, conn, databaseWrapper, org.PublicId, oidc.ActivePrivateState, "alice-rp", "fido",
		oidc.WithIssuer(oidc.TestConvertToUrls(t, "https://alice.com")[0]),
		oidc.WithSigningAlgs(oidc.RS256),
		oidc.WithApiUrl(oidc.TestConvertToUrls(t, "http://localhost")[0]))

	iam.TestSetPrimaryAuthMethod(t, repo, org, authMethod.PublicId)

	type args struct {
		withOrgId string
		opt       []iam.Option
	}
	tests := []struct {
		name      string
		createCnt int
		args      args
		wantCnt   int
		wantErr   bool
	}{
		{
			name:      "no-limit",
			createCnt: testLimit + 1,
			args: args{
				withOrgId: org.PublicId,
				opt:       []iam.Option{iam.WithLimit(-1)},
			},
			wantCnt: testLimit + 1,
			wantErr: false,
		},
		{
			name:      "default-limit",
			createCnt: testLimit + 1,
			args: args{
				withOrgId: org.PublicId,
			},
			wantCnt: testLimit,
			wantErr: false,
		},
		{
			name:      "custom-limit",
			createCnt: testLimit + 1,
			args: args{
				withOrgId: org.PublicId,
				opt:       []iam.Option{iam.WithLimit(3)},
			},
			wantCnt: 3,
			wantErr: false,
		},
		{
			name:      "bad-org",
			createCnt: 1,
			args: args{
				withOrgId: "bad-id",
			},
			wantCnt: 0,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			require.NoError(conn.Where("public_id != 'u_anon' and public_id != 'u_auth' and public_id != 'u_recovery'").Delete(iam.AllocUser()).Error)
			testUsers := []*iam.User{}
			wantEmail, wantFullName := fmt.Sprintf("%s@example.com", tt.name), tt.name
			for i := 0; i < tt.createCnt; i++ {
				u := iam.TestUser(t, repo, org.PublicId)
				testUsers = append(testUsers, u)
				a := oidc.TestAccount(t, conn, authMethod, fmt.Sprintf(tt.name, i), oidc.WithFullName(wantFullName), oidc.WithEmail(wantEmail))
				_, err := repo.AddUserAccounts(context.Background(), u.PublicId, u.Version, []string{a.PublicId})
				require.NoError(err)
			}
			assert.Equal(tt.createCnt, len(testUsers))
			got, err := repo.ListUsers(context.Background(), []string{tt.args.withOrgId}, tt.args.opt...)
			if tt.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
			assert.Equal(tt.wantCnt, len(got))
			for _, u := range got {
				assert.Equal(wantFullName, u.FullName)
				assert.Equal(wantEmail, u.Email)
			}
		})
	}
}

func TestRepository_ListUsers_Multiple_Scopes(t *testing.T) {
	t.Parallel()
	conn, _ := db.TestSetup(t, "postgres")
	wrapper := db.TestWrapper(t)
	repo := iam.TestRepo(t, conn, wrapper)
	org, _ := iam.TestScopes(t, repo)

	require.NoError(t, conn.Where("public_id != 'u_anon' and public_id != 'u_auth' and public_id != 'u_recovery'").Delete(iam.AllocUser()).Error)

	const numPerScope = 10
	var total int = 3 // anon, auth, recovery
	for i := 0; i < numPerScope; i++ {
		iam.TestUser(t, repo, "global")
		total++
		iam.TestUser(t, repo, org.GetPublicId())
		total++
	}

	got, err := repo.ListUsers(context.Background(), []string{"global", org.GetPublicId()})
	require.NoError(t, err)
	assert.Equal(t, total, len(got))
}

// func TestRepository_LookupUserWithLogin(t *testing.T) {
// 	t.Parallel()
// 	conn, _ := db.TestSetup(t, "postgres")
// 	rw := db.New(conn)
// 	wrapper := db.TestWrapper(t)
// 	repo := TestRepo(t, conn, wrapper)

// 	id := testId(t)
// 	org, _ := TestScopes(t, repo)
// 	authMethodId := testAuthMethod(t, conn, org.PublicId)
// 	TestSetPrimaryAuthMethod(t, repo, org, authMethodId)
// 	newAuthAcct := testAccount(t, conn, org.PublicId, authMethodId, "")

// 	authMethodId2 := testAuthMethod(t, conn, org.PublicId)
// 	newAuthAcctWithoutVivify := testAccount(t, conn, org.PublicId, authMethodId2, "")

// 	user := TestUser(t, repo, org.PublicId, WithName("existing-"+id))
// 	existingUserWithAcctWithVivify := testAccount(t, conn, org.PublicId, authMethodId, user.PublicId)
// 	require.Equal(t, user.PublicId, existingUserWithAcctWithVivify.IamUserId)

// 	existingUserWithAcctNoVivify := testAccount(t, conn, org.PublicId, authMethodId2, user.PublicId)

// 	type args struct {
// 		withAccountId string
// 		opt           []Option
// 	}
// 	tests := []struct {
// 		name            string
// 		args            args
// 		wantName        string
// 		wantDescription string
// 		wantErr         bool
// 		wantErrIs       errors.Code
// 		wantUser        *User
// 	}{
// 		{
// 			name: "valid",
// 			args: args{
// 				withAccountId: newAuthAcct.PublicId,
// 				opt: []Option{
// 					WithName("valid-" + id),
// 					WithDescription("valid-" + id),
// 				},
// 			},
// 			wantName:        "valid-" + id,
// 			wantDescription: "valid-" + id,
// 			wantErr:         false,
// 		},
// 		{
// 			name: "new-acct-without-vivify",
// 			args: args{
// 				withAccountId: newAuthAcctWithoutVivify.PublicId,
// 			},
// 			wantErr:   true,
// 			wantErrIs: errors.RecordNotFound,
// 		},
// 		{
// 			name: "missing auth acct id",
// 			args: args{
// 				withAccountId: "",
// 			},
// 			wantErr:   true,
// 			wantErrIs: errors.InvalidParameter,
// 		},
// 		{
// 			name: "existing-user-with-account-with-vivify",
// 			args: args{
// 				withAccountId: existingUserWithAcctWithVivify.PublicId,
// 			},
// 			wantErr:  false,
// 			wantName: "existing-" + id,
// 			wantUser: user,
// 		},
// 		{
// 			name: "existing-user-with-account-no-vivify",
// 			args: args{
// 				withAccountId: existingUserWithAcctNoVivify.PublicId,
// 				opt:           []Option{},
// 			},
// 			wantErr:  false,
// 			wantName: "existing-" + id,
// 			wantUser: user,
// 		},
// 		{
// 			name: "bad-auth-account-id",
// 			args: args{
// 				withAccountId: id,
// 			},
// 			wantErr:   true,
// 			wantErrIs: errors.RecordNotFound,
// 		},
// 		{
// 			name: "bad-auth-account-id-with-vivify",
// 			args: args{
// 				withAccountId: id,
// 				opt:           []Option{},
// 			},
// 			wantErr:   true,
// 			wantErrIs: errors.RecordNotFound,
// 		},
// 	}
// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			assert, require := assert.New(t), require.New(t)
// 			dbassert := dbassert.New(t, conn.DB())
// 			got, err := repo.LookupUserWithLogin(context.Background(), tt.args.withAccountId, tt.args.opt...)
// 			if tt.wantErr {
// 				require.Error(err)
// 				assert.Nil(got)
// 				assert.Truef(errors.Match(errors.T(tt.wantErrIs), err), "unexpected error %s", err.Error())
// 				if tt.args.withAccountId != "" && tt.args.withAccountId != id {
// 					// need to assert that userid in auth_account is still null
// 					acct := allocAccount()
// 					acct.PublicId = tt.args.withAccountId
// 					dbassert.IsNull(&acct, "IamUserId")
// 				}
// 				return
// 			}
// 			require.NoError(err)
// 			if tt.wantName != "" {
// 				assert.Equal(tt.wantName, got.Name)
// 			}
// 			if tt.wantDescription != "" {
// 				assert.Equal(tt.wantDescription, got.Description)
// 			}
// 			require.NotEmpty(got.PublicId)
// 			if tt.wantUser != nil {
// 				assert.True(proto.Equal(tt.wantUser.User, got.User))
// 			}
// 			acct := allocAccount()
// 			acct.PublicId = tt.args.withAccountId
// 			err = rw.LookupByPublicId(context.Background(), &acct)
// 			require.NoError(err)
// 			assert.Equal(got.PublicId, acct.IamUserId)
// 		})
// 	}
// }

// func TestRepository_AssociateAccounts(t *testing.T) {
// 	t.Parallel()
// 	conn, _ := db.TestSetup(t, "postgres")
// 	rw := db.New(conn)
// 	wrapper := db.TestWrapper(t)
// 	repo := TestRepo(t, conn, wrapper)
// 	org, _ := TestScopes(t, repo)
// 	authMethodId := testAuthMethod(t, conn, org.PublicId)
// 	user := TestUser(t, repo, org.PublicId)

// 	createAccountsFn := func() []string {
// 		require.NoError(t, conn.Where("iam_user_id = ?", user.PublicId).Delete(allocAccount()).Error)
// 		results := []string{}
// 		for i := 0; i < 5; i++ {
// 			a := testAccount(t, conn, org.PublicId, authMethodId, "")
// 			results = append(results, a.PublicId)
// 		}
// 		return results
// 	}
// 	type args struct {
// 		accountIdsFn        func() []string
// 		userId              string
// 		userVersionOverride *uint32
// 		opt                 []Option
// 	}
// 	tests := []struct {
// 		name        string
// 		args        args
// 		wantErr     bool
// 		wantErrCode errors.Code
// 	}{
// 		{
// 			name: "valid",
// 			args: args{
// 				userId:       user.PublicId,
// 				accountIdsFn: createAccountsFn,
// 			},
// 			wantErr: false,
// 		},
// 		{
// 			name: "already-associated",
// 			args: args{
// 				userId: user.PublicId,
// 				accountIdsFn: func() []string {
// 					ids := createAccountsFn()
// 					a := testAccount(t, conn, org.PublicId, authMethodId, user.PublicId)
// 					ids = append(ids, a.PublicId)
// 					return ids
// 				},
// 			},
// 			wantErr: false,
// 		},
// 		{
// 			name: "associated-with-diff-user",
// 			args: args{
// 				userId: user.PublicId,
// 				accountIdsFn: func() []string {
// 					ids := createAccountsFn()
// 					u := TestUser(t, repo, org.PublicId)
// 					a := testAccount(t, conn, org.PublicId, authMethodId, u.PublicId)
// 					ids = append(ids, a.PublicId)
// 					return ids
// 				},
// 			},
// 			wantErr:     true,
// 			wantErrCode: errors.AccountAlreadyAssociated,
// 		},
// 		{
// 			name: "bad-version",
// 			args: args{
// 				userVersionOverride: func() *uint32 {
// 					i := uint32(22)
// 					return &i
// 				}(),
// 				userId:       user.PublicId,
// 				accountIdsFn: createAccountsFn,
// 			},
// 			wantErr:     true,
// 			wantErrCode: errors.MultipleRecords,
// 		},
// 		{
// 			name: "zero-version",
// 			args: args{
// 				userVersionOverride: func() *uint32 {
// 					i := uint32(0)
// 					return &i
// 				}(),
// 				userId:       user.PublicId,
// 				accountIdsFn: createAccountsFn,
// 			},
// 			wantErr:     true,
// 			wantErrCode: errors.InvalidParameter,
// 		},
// 		{
// 			name: "no-accounts",
// 			args: args{
// 				userId:       user.PublicId,
// 				accountIdsFn: func() []string { return nil },
// 			},
// 			wantErr:     true,
// 			wantErrCode: errors.InvalidParameter,
// 		},
// 	}
// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			assert, require := assert.New(t), require.New(t)
// 			accountIds := tt.args.accountIdsFn()
// 			sort.Strings(accountIds)

// 			origUser, _, err := repo.LookupUser(context.Background(), user.PublicId)
// 			require.NoError(err)

// 			version := origUser.Version
// 			if tt.args.userVersionOverride != nil {
// 				version = *tt.args.userVersionOverride
// 			}

// 			got, err := repo.AddUserAccounts(context.Background(), tt.args.userId, version, accountIds, tt.args.opt...)
// 			if tt.wantErr {
// 				require.Error(err)
// 				assert.Truef(errors.Match(errors.T(tt.wantErrCode), err), "unexpected error %s", err)
// 				return
// 			}
// 			require.NoError(err)
// 			err = db.TestVerifyOplog(t, rw, tt.args.userId, db.WithOperation(oplog.OpType_OP_TYPE_UPDATE), db.WithCreateNotBefore(10*time.Second))
// 			assert.NoError(err)
// 			for _, id := range got {
// 				err = db.TestVerifyOplog(t, rw, id, db.WithOperation(oplog.OpType_OP_TYPE_UPDATE), db.WithCreateNotBefore(10*time.Second))
// 				assert.NoError(err)
// 			}

// 			sort.Strings(got)
// 			assert.Equal(accountIds, got)

// 			foundIds, err := repo.ListUserAccounts(context.Background(), tt.args.userId)
// 			require.NoError(err)
// 			sort.Strings(foundIds)
// 			assert.Equal(accountIds, foundIds)

// 			u, _, err := repo.LookupUser(context.Background(), tt.args.userId)
// 			require.NoError(err)
// 			assert.Equal(version+1, u.Version)
// 		})
// 	}
// }

// func TestRepository_DisassociateAccounts(t *testing.T) {
// 	t.Parallel()
// 	conn, _ := db.TestSetup(t, "postgres")
// 	rw := db.New(conn)
// 	wrapper := db.TestWrapper(t)
// 	repo := TestRepo(t, conn, wrapper)
// 	org, _ := TestScopes(t, repo)
// 	authMethodId := testAuthMethod(t, conn, org.PublicId)
// 	user := TestUser(t, repo, org.PublicId)

// 	createAccountsFn := func() []string {
// 		require.NoError(t, conn.Where("iam_user_id = ?", user.PublicId).Delete(allocAccount()).Error)
// 		results := []string{}
// 		for i := 0; i < 1; i++ {
// 			a := testAccount(t, conn, org.PublicId, authMethodId, user.PublicId)
// 			results = append(results, a.PublicId)
// 		}
// 		return results
// 	}
// 	type args struct {
// 		accountIdsFn        func() []string
// 		userId              string
// 		userVersionOverride *uint32
// 		opt                 []Option
// 	}
// 	tests := []struct {
// 		name        string
// 		args        args
// 		wantErr     bool
// 		wantErrCode errors.Code
// 	}{
// 		{
// 			name: "valid",
// 			args: args{
// 				userId:       user.PublicId,
// 				accountIdsFn: createAccountsFn,
// 			},
// 			wantErr: false,
// 		},
// 		{
// 			name: "associated-with-diff-user",
// 			args: args{
// 				userId: user.PublicId,
// 				accountIdsFn: func() []string {
// 					ids := createAccountsFn()
// 					u := TestUser(t, repo, org.PublicId)
// 					a := testAccount(t, conn, org.PublicId, authMethodId, u.PublicId)
// 					ids = append(ids, a.PublicId)
// 					return ids
// 				},
// 			},
// 			wantErr:     true,
// 			wantErrCode: errors.AccountAlreadyAssociated,
// 		},
// 		{
// 			name: "bad-version",
// 			args: args{
// 				userVersionOverride: func() *uint32 {
// 					i := uint32(22)
// 					return &i
// 				}(),
// 				userId:       user.PublicId,
// 				accountIdsFn: createAccountsFn,
// 			},
// 			wantErr:     true,
// 			wantErrCode: errors.MultipleRecords,
// 		},
// 		{
// 			name: "zero-version",
// 			args: args{
// 				userVersionOverride: func() *uint32 {
// 					i := uint32(0)
// 					return &i
// 				}(),
// 				userId:       user.PublicId,
// 				accountIdsFn: createAccountsFn,
// 			},
// 			wantErr:     true,
// 			wantErrCode: errors.InvalidParameter,
// 		},
// 		{
// 			name: "no-accounts",
// 			args: args{
// 				userId:       user.PublicId,
// 				accountIdsFn: func() []string { return nil },
// 			},
// 			wantErr:     true,
// 			wantErrCode: errors.InvalidParameter,
// 		},
// 	}
// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			assert, require := assert.New(t), require.New(t)
// 			accountIds := tt.args.accountIdsFn()

// 			origUser, _, err := repo.LookupUser(context.Background(), user.PublicId)
// 			require.NoError(err)

// 			version := origUser.Version
// 			if tt.args.userVersionOverride != nil {
// 				version = *tt.args.userVersionOverride
// 			}

// 			got, err := repo.DeleteUserAccounts(context.Background(), tt.args.userId, version, accountIds, tt.args.opt...)
// 			if tt.wantErr {
// 				require.Error(err)
// 				assert.Truef(errors.Match(errors.T(tt.wantErrCode), err), "unexpected error %s", err)
// 				return
// 			}
// 			require.NoError(err)
// 			err = db.TestVerifyOplog(t, rw, tt.args.userId, db.WithOperation(oplog.OpType_OP_TYPE_UPDATE), db.WithCreateNotBefore(10*time.Second))
// 			assert.NoError(err)
// 			for _, id := range got {
// 				err = db.TestVerifyOplog(t, rw, id, db.WithOperation(oplog.OpType_OP_TYPE_UPDATE), db.WithCreateNotBefore(10*time.Second))
// 				assert.NoError(err)
// 			}
// 			foundIds, err := repo.ListUserAccounts(context.Background(), tt.args.userId)
// 			require.NoError(err)
// 			for _, id := range accountIds {
// 				assert.True(!strutil.StrListContains(foundIds, id))
// 			}

// 			u, _, err := repo.LookupUser(context.Background(), tt.args.userId)
// 			require.NoError(err)
// 			assert.Equal(version+1, u.Version)
// 		})
// 	}
// }

// func TestRepository_SetAssociatedAccounts(t *testing.T) {
// 	t.Parallel()
// 	conn, _ := db.TestSetup(t, "postgres")
// 	rw := db.New(conn)
// 	wrapper := db.TestWrapper(t)
// 	repo := TestRepo(t, conn, wrapper)
// 	org, _ := TestScopes(t, repo)
// 	authMethodId := testAuthMethod(t, conn, org.PublicId)
// 	user := TestUser(t, repo, org.PublicId)

// 	createAccountsFn := func() []string {
// 		require.NoError(t, conn.Where("iam_user_id = ?", user.PublicId).Delete(allocAccount()).Error)
// 		results := []string{}
// 		for i := 0; i < 5; i++ {
// 			a := testAccount(t, conn, org.PublicId, authMethodId, "")
// 			results = append(results, a.PublicId)
// 		}
// 		return results
// 	}
// 	type args struct {
// 		accountIdsFn        func() ([]string, []string)
// 		userId              string
// 		userVersionOverride *uint32
// 		opt                 []Option
// 	}
// 	tests := []struct {
// 		name        string
// 		args        args
// 		wantErr     bool
// 		wantErrCode errors.Code
// 	}{
// 		{
// 			name: "valid",
// 			args: args{
// 				userId: user.PublicId,
// 				accountIdsFn: func() ([]string, []string) {
// 					ids := createAccountsFn()
// 					return ids, ids
// 				},
// 			},
// 			wantErr: false,
// 		},
// 		{
// 			name: "one-already-associated",
// 			args: args{
// 				userId: user.PublicId,
// 				accountIdsFn: func() ([]string, []string) {
// 					ids := createAccountsFn()
// 					changes := append([]string{}, ids...)
// 					a := testAccount(t, conn, org.PublicId, authMethodId, user.PublicId)
// 					ids = append(ids, a.PublicId)
// 					return ids, changes
// 				},
// 			},
// 			wantErr: false,
// 		},
// 		{
// 			name: "no-change",
// 			args: args{
// 				userId: user.PublicId,
// 				accountIdsFn: func() ([]string, []string) {
// 					ids := []string{}
// 					for i := 0; i < 10; i++ {
// 						a := testAccount(t, conn, org.PublicId, authMethodId, user.PublicId)
// 						ids = append(ids, a.PublicId)
// 					}
// 					return ids, nil
// 				},
// 			},
// 			wantErr: false,
// 		},
// 		{
// 			name: "remove-all",
// 			args: args{
// 				userId: user.PublicId,
// 				accountIdsFn: func() ([]string, []string) {
// 					ids := []string{}
// 					for i := 0; i < 10; i++ {
// 						a := testAccount(t, conn, org.PublicId, authMethodId, user.PublicId)
// 						ids = append(ids, a.PublicId)
// 					}
// 					return nil, ids
// 				},
// 			},
// 			wantErr: false,
// 		},
// 		{
// 			name: "associated-with-diff-user",
// 			args: args{
// 				userId: user.PublicId,
// 				accountIdsFn: func() ([]string, []string) {
// 					ids := createAccountsFn()
// 					u := TestUser(t, repo, org.PublicId)
// 					a := testAccount(t, conn, org.PublicId, authMethodId, u.PublicId)
// 					ids = append(ids, a.PublicId)
// 					return ids, ids
// 				},
// 			},
// 			wantErr:     true,
// 			wantErrCode: errors.AccountAlreadyAssociated,
// 		},
// 		{
// 			name: "bad-version",
// 			args: args{
// 				userVersionOverride: func() *uint32 {
// 					i := uint32(22)
// 					return &i
// 				}(),
// 				userId: user.PublicId,
// 				accountIdsFn: func() ([]string, []string) {
// 					ids := createAccountsFn()
// 					return ids, ids
// 				},
// 			},
// 			wantErr:     true,
// 			wantErrCode: errors.MultipleRecords,
// 		},
// 		{
// 			name: "zero-version",
// 			args: args{
// 				userVersionOverride: func() *uint32 {
// 					i := uint32(0)
// 					return &i
// 				}(),
// 				userId: user.PublicId,
// 				accountIdsFn: func() ([]string, []string) {
// 					ids := createAccountsFn()
// 					return ids, ids
// 				},
// 			},
// 			wantErr:     true,
// 			wantErrCode: errors.InvalidParameter,
// 		},
// 		{
// 			name: "no-accounts-no-changes",
// 			args: args{
// 				userId:       user.PublicId,
// 				accountIdsFn: func() ([]string, []string) { return nil, nil },
// 			},
// 			wantErr: false,
// 		},
// 	}
// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			assert, require := assert.New(t), require.New(t)
// 			accountIds, changes := tt.args.accountIdsFn()
// 			sort.Strings(accountIds)

// 			origUser, _, err := repo.LookupUser(context.Background(), user.PublicId)
// 			require.NoError(err)

// 			version := origUser.Version
// 			if tt.args.userVersionOverride != nil {
// 				version = *tt.args.userVersionOverride
// 			}

// 			got, err := repo.SetUserAccounts(context.Background(), tt.args.userId, version, accountIds, tt.args.opt...)
// 			if tt.wantErr {
// 				require.Error(err)
// 				assert.Truef(errors.Match(errors.T(tt.wantErrCode), err), "unexpected error %s", err)
// 				return
// 			}
// 			require.NoError(err)
// 			if len(changes) != 0 {
// 				err = db.TestVerifyOplog(t, rw, tt.args.userId, db.WithOperation(oplog.OpType_OP_TYPE_UPDATE), db.WithCreateNotBefore(10*time.Second))
// 				assert.NoError(err)
// 				for _, id := range changes {
// 					err = db.TestVerifyOplog(t, rw, id, db.WithOperation(oplog.OpType_OP_TYPE_UPDATE), db.WithCreateNotBefore(10*time.Second))
// 					assert.NoErrorf(err, "%s missing oplog entry", id)
// 				}
// 			}

// 			sort.Strings(got)
// 			assert.Equal(accountIds, got)

// 			foundIds, err := repo.ListUserAccounts(context.Background(), tt.args.userId)
// 			require.NoError(err)
// 			sort.Strings(foundIds)
// 			assert.Equal(accountIds, foundIds)

// 			u, _, err := repo.LookupUser(context.Background(), tt.args.userId)
// 			require.NoError(err)
// 			switch tt.name {
// 			case "no-accounts-no-changes":
// 				assert.Equal(version, u.Version)
// 			default:
// 				assert.Equal(version+1, u.Version)
// 			}
// 		})
// 	}
// }

func testId(t *testing.T) string {
	t.Helper()
	id, err := uuid.GenerateUUID()
	require.NoError(t, err)
	return id
}
