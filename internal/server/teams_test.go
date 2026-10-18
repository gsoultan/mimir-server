package server_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// TestTeams keeps two teams apart on one server: neither sees the other's
// models, examples, runs, webhooks, people or audit events, and only
// owners manage teams and server settings.
func TestTeams(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	svc.AllowPrivateWebhooks = true
	own := owner(t, srv)

	// The server starts with one team that the owner is in.
	who, _ := own.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{}))
	home := who.Msg.GetUser().GetTeamId()
	if home == "" || who.Msg.GetUser().GetTeamName() != "My team" {
		t.Fatalf("owner's team = %v", who.Msg.GetUser())
	}
	if _, err := own.teams.CreateTeam(ctx, connect.NewRequest(&mimirv1.CreateTeamRequest{Name: " "})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("nameless team: %v", err)
	}
	lab, err := own.teams.CreateTeam(ctx, connect.NewRequest(&mimirv1.CreateTeamRequest{Name: "Lab"}))
	if err != nil {
		t.Fatal(err)
	}
	labID := lab.Msg.GetTeam().GetId()
	if _, err := own.teams.CreateTeam(ctx, connect.NewRequest(&mimirv1.CreateTeamRequest{Name: "Lab"})); code(err) != connect.CodeAlreadyExists {
		t.Fatalf("duplicate team: %v", err)
	}

	// Things in the owner's team.
	m, err := own.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Home digits", Architecture: "mnist-mlp", Weights: weights(t, "mnist-mlp")}))
	if err != nil {
		t.Fatal(err)
	}
	homeModel := m.Msg.GetModel().GetId()
	ds, err := own.datasets.CreateDataset(ctx, connect.NewRequest(&mimirv1.CreateDatasetRequest{Name: "Fruit", Kind: mimirv1.DatasetKind_DATASET_KIND_TABLE, Csv: []byte(blobs(100))}))
	if err != nil {
		t.Fatal(err)
	}
	job, err := own.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Sorter", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId(), Settings: &mimirv1.TrainingSettings{Epochs: 2}}))
	if err != nil {
		t.Fatal(err)
	}
	hook, err := own.hooks.CreateWebhook(ctx, connect.NewRequest(&mimirv1.CreateWebhookRequest{Name: "Home hook", Url: "http://127.0.0.1:1/home"}))
	if err != nil {
		t.Fatal(err)
	}

	// An admin in the Lab, added by the owner.
	if _, err := own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "a@lab.test", Name: "Lab admin", Password: password, TeamId: labID, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_ADMIN}})); err != nil {
		t.Fatal(err)
	}
	// Granting a model from another team is refused.
	if _, err := own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "v@lab.test", Name: "Lab viewer", Password: password, TeamId: labID, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, ModelIds: []string{homeModel}}})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("grant across teams: %v", err)
	}
	a := signIn(t, srv, "a@lab.test")
	me, _ := a.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{}))
	if me.Msg.GetUser().GetTeamName() != "Lab" {
		t.Fatalf("lab admin's team = %v", me.Msg.GetUser())
	}

	// The Lab sees none of the home team's things, by list or by id.
	if ms, _ := a.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); len(ms.Msg.GetModels()) != 0 {
		t.Fatalf("lab sees models %v", ms.Msg.GetModels())
	}
	if _, err := a.models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: homeModel})); code(err) != connect.CodeNotFound {
		t.Fatalf("lab GetModel: %v", err)
	}
	if _, err := a.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: homeModel, Input: &mimirv1.Tensor{Values: make([]float32, 784)}})); code(err) != connect.CodeNotFound {
		t.Fatalf("lab Predict: %v", err)
	}
	if d, _ := a.datasets.ListDatasets(ctx, connect.NewRequest(&mimirv1.ListDatasetsRequest{})); len(d.Msg.GetDatasets()) != 0 {
		t.Fatalf("lab sees datasets")
	}
	if _, err := a.datasets.GetDataset(ctx, connect.NewRequest(&mimirv1.GetDatasetRequest{Id: ds.Msg.GetDataset().GetId()})); code(err) != connect.CodeNotFound {
		t.Fatalf("lab GetDataset: %v", err)
	}
	if _, err := a.training.CreateJob(ctx, connect.NewRequest(&mimirv1.CreateJobRequest{Name: "Steal", Architecture: "table-mlp", DatasetId: ds.Msg.GetDataset().GetId()})); code(err) != connect.CodeNotFound {
		t.Fatalf("lab trains on home examples: %v", err)
	}
	if j, _ := a.training.ListJobs(ctx, connect.NewRequest(&mimirv1.ListJobsRequest{})); len(j.Msg.GetJobs()) != 0 {
		t.Fatalf("lab sees jobs")
	}
	if _, err := a.training.CancelJob(ctx, connect.NewRequest(&mimirv1.CancelJobRequest{Id: job.Msg.GetJob().GetId()})); code(err) != connect.CodeNotFound {
		t.Fatalf("lab CancelJob: %v", err)
	}
	if h, _ := a.hooks.ListWebhooks(ctx, connect.NewRequest(&mimirv1.ListWebhooksRequest{})); len(h.Msg.GetWebhooks()) != 0 {
		t.Fatalf("lab sees webhooks")
	}
	if _, err := a.hooks.DeleteWebhook(ctx, connect.NewRequest(&mimirv1.DeleteWebhookRequest{Id: hook.Msg.GetWebhook().GetId()})); code(err) != connect.CodeNotFound {
		t.Fatalf("lab DeleteWebhook: %v", err)
	}
	us, _ := a.users.ListUsers(ctx, connect.NewRequest(&mimirv1.ListUsersRequest{}))
	if len(us.Msg.GetUsers()) != 1 || us.Msg.GetUsers()[0].GetEmail() != "a@lab.test" {
		t.Fatalf("lab sees users %v", us.Msg.GetUsers())
	}
	if _, err := a.users.DeleteUser(ctx, connect.NewRequest(&mimirv1.DeleteUserRequest{Id: who.Msg.GetUser().GetId()})); code(err) != connect.CodeNotFound {
		t.Fatalf("lab deletes the owner: %v", err)
	}
	ev, _ := a.audit.ListAuditEvents(ctx, connect.NewRequest(&mimirv1.ListAuditEventsRequest{}))
	for _, e := range ev.Msg.GetEvents() {
		if e.GetTeamName() != "Lab" {
			t.Fatalf("lab sees audit event %v", e)
		}
	}

	// Admins run their team; the server's settings, workers and teams are
	// the owners'.
	if _, err := a.settings.GetSettings(ctx, connect.NewRequest(&mimirv1.GetSettingsRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("admin GetSettings: %v", err)
	}
	if _, err := a.workers.ListWorkers(ctx, connect.NewRequest(&mimirv1.ListWorkersRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("admin ListWorkers: %v", err)
	}
	if _, err := a.teams.ListTeams(ctx, connect.NewRequest(&mimirv1.ListTeamsRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("admin ListTeams: %v", err)
	}
	if _, err := a.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "x@lab.test", Name: "X", Password: password, TeamId: home, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER}})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("admin adds someone to another team: %v", err)
	}

	// The Lab's own work stays in the Lab, and its webhooks hear only it.
	labHook, err := a.hooks.CreateWebhook(ctx, connect.NewRequest(&mimirv1.CreateWebhookRequest{Name: "Lab hook", Url: "http://127.0.0.1:1/lab"}))
	if err != nil {
		t.Fatal(err)
	}
	lm, err := a.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Lab digits", Architecture: "mnist-mlp", Weights: weights(t, "mnist-mlp")}))
	if err != nil {
		t.Fatal(err)
	}
	if ms, _ := own.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); len(ms.Msg.GetModels()) != 1 || ms.Msg.GetModels()[0].GetId() != homeModel {
		t.Fatalf("owner's team models = %v", ms.Msg.GetModels())
	}
	labDel, _ := a.hooks.ListDeliveries(ctx, connect.NewRequest(&mimirv1.ListDeliveriesRequest{WebhookId: labHook.Msg.GetWebhook().GetId()}))
	homeDel, _ := own.hooks.ListDeliveries(ctx, connect.NewRequest(&mimirv1.ListDeliveriesRequest{WebhookId: hook.Msg.GetWebhook().GetId()}))
	if len(labDel.Msg.GetDeliveries()) != 1 || labDel.Msg.GetDeliveries()[0].GetEventType() != "model.created" {
		t.Fatalf("lab deliveries = %v", labDel.Msg.GetDeliveries())
	}
	for _, d := range homeDel.Msg.GetDeliveries() {
		if d.GetEventType() == "model.created" {
			t.Fatalf("home hook heard the lab's model")
		}
	}

	// Owners see everyone, with their team.
	all, _ := own.users.ListUsers(ctx, connect.NewRequest(&mimirv1.ListUsersRequest{}))
	if len(all.Msg.GetUsers()) != 2 || all.Msg.GetUsers()[1].GetTeamName() != "Lab" {
		t.Fatalf("owner ListUsers = %v", all.Msg.GetUsers())
	}
	ts, _ := own.teams.ListTeams(ctx, connect.NewRequest(&mimirv1.ListTeamsRequest{}))
	if len(ts.Msg.GetTeams()) != 2 {
		t.Fatalf("ListTeams = %v", ts.Msg.GetTeams())
	}
	for _, tm := range ts.Msg.GetTeams() {
		if tm.GetMembers() != 1 || tm.GetModels() != 1 {
			t.Fatalf("team counts %v", tm)
		}
	}

	// A team with people or models can't be deleted; an empty one can.
	if _, err := own.teams.DeleteTeam(ctx, connect.NewRequest(&mimirv1.DeleteTeamRequest{Id: labID})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("delete busy team: %v", err)
	}
	if _, err := own.teams.UpdateTeam(ctx, connect.NewRequest(&mimirv1.UpdateTeamRequest{Id: labID, Name: "Research"})); err != nil {
		t.Fatal(err)
	}

	// Moving someone signs them out and revokes their keys.
	key, err := a.keys.CreateApiKey(ctx, connect.NewRequest(&mimirv1.CreateApiKeyRequest{Name: "app", Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}}))
	if err != nil {
		t.Fatal(err)
	}
	labAdmin := all.Msg.GetUsers()[1]
	if _, err := own.users.UpdateUser(ctx, connect.NewRequest(&mimirv1.UpdateUserRequest{Id: labAdmin.GetId(), Name: labAdmin.GetName(), Access: labAdmin.GetAccess(), TeamId: home})); err != nil {
		t.Fatal(err)
	}
	if _, err := a.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("moved person still signed in: %v", err)
	}
	app := newClients(srv, connect.WithInterceptors(bearer(key.Msg.GetSecret())))
	if _, err := app.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("moved person's key still works: %v", err)
	}
	a = signIn(t, srv, "a@lab.test")
	if ms, _ := a.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); len(ms.Msg.GetModels()) != 1 || ms.Msg.GetModels()[0].GetId() != homeModel {
		t.Fatalf("after the move = %v", ms.Msg.GetModels())
	}

	// Emptied, the old team can go; its webhooks go with it.
	if _, err := own.users.UpdateUser(ctx, connect.NewRequest(&mimirv1.UpdateUserRequest{Id: who.Msg.GetUser().GetId(), Name: "Owner", Access: who.Msg.GetUser().GetAccess(), TeamId: labID})); err != nil {
		t.Fatal(err)
	}
	// The owner moved themselves and is still signed in, now in the old Lab.
	if _, err := own.models.DeleteModel(ctx, connect.NewRequest(&mimirv1.DeleteModelRequest{Id: lm.Msg.GetModel().GetId()})); err != nil {
		t.Fatalf("owner after moving: %v", err)
	}
	if _, err := own.users.UpdateUser(ctx, connect.NewRequest(&mimirv1.UpdateUserRequest{Id: who.Msg.GetUser().GetId(), Name: "Owner", Access: who.Msg.GetUser().GetAccess(), TeamId: home})); err != nil {
		t.Fatal(err)
	}
	if _, err := own.teams.DeleteTeam(ctx, connect.NewRequest(&mimirv1.DeleteTeamRequest{Id: labID})); err != nil {
		t.Fatalf("delete empty team: %v", err)
	}
	ev, _ = own.audit.ListAuditEvents(ctx, connect.NewRequest(&mimirv1.ListAuditEventsRequest{}))
	seen := map[string]bool{}
	for _, e := range ev.Msg.GetEvents() {
		seen[e.GetAction()] = true
	}
	for _, want := range []string{"team.create", "team.rename", "user.move", "team.delete"} {
		if !seen[want] {
			t.Errorf("audit lacks %s", want)
		}
	}
}
