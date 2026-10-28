import { createConnectQueryKey, createInfiniteQueryOptions, createQueryOptions } from "@connectrpc/connect-query";

import { ApiKeyService, AuditService, AuthService, UserService } from "./gen/mimir/v1/access_pb";
import { ModelService } from "./gen/mimir/v1/model_pb";
import { SettingsService } from "./gen/mimir/v1/settings_pb";
import { SignInService } from "./gen/mimir/v1/signin_pb";
import { BenchmarkService } from "./gen/mimir/v1/benchmark_pb";
import { ScoringService } from "./gen/mimir/v1/scoring_pb";
import { DatasetService, TrainingService } from "./gen/mimir/v1/training_pb";
import { WebhookService } from "./gen/mimir/v1/webhook_pb";
import { TeamService } from "./gen/mimir/v1/team_pb";
import { TemplateService } from "./gen/mimir/v1/template_pb";
import { WorkerService } from "./gen/mimir/v1/worker_pb";
import { transport } from "./client";

export const listModels = createQueryOptions(ModelService.method.listModels, {}, { transport });
export const listArchitectures = createQueryOptions(ModelService.method.listArchitectures, {}, { transport });
export const getModel = (id: string) => createQueryOptions(ModelService.method.getModel, { id }, { transport });
export const modelQuality = (modelId: string) => createQueryOptions(ModelService.method.getModelQuality, { modelId }, { transport });
export const modelVersions = (modelId: string) => createQueryOptions(ModelService.method.listModelVersions, { modelId }, { transport });

export const setupState = createQueryOptions(AuthService.method.getSetupState, {}, { transport });
export const whoAmI = createQueryOptions(AuthService.method.whoAmI, {}, { transport });
export const listSessions = createQueryOptions(AuthService.method.listSessions, {}, { transport });
export const listUsers = createQueryOptions(UserService.method.listUsers, {}, { transport });
export const listApiKeys = createQueryOptions(ApiKeyService.method.listApiKeys, {}, { transport });
export const auditLog = createInfiniteQueryOptions(AuditService.method.listAuditEvents, { pageSize: 50, pageToken: "" }, {
  transport,
  pageParamKey: "pageToken",
  getNextPageParam: (last) => last.nextPageToken || undefined,
});

export const listWebhooks = createQueryOptions(WebhookService.method.listWebhooks, {}, { transport });
export const deliveries = (webhookId: string) =>
  createInfiniteQueryOptions(WebhookService.method.listDeliveries, { webhookId, pageSize: 25, pageToken: "" }, {
    transport,
    pageParamKey: "pageToken",
    getNextPageParam: (last) => last.nextPageToken || undefined,
  });
export const settings = createQueryOptions(SettingsService.method.getSettings, {}, { transport });
export const databaseInfo = createQueryOptions(SettingsService.method.getDatabaseInfo, {}, { transport });

export const listDatasets = createQueryOptions(DatasetService.method.listDatasets, {}, { transport });
export const getDataset = (id: string) => createQueryOptions(DatasetService.method.getDataset, { id }, { transport });
export const listJobs = createQueryOptions(TrainingService.method.listJobs, {}, { transport });
export const getJob = (id: string) => createQueryOptions(TrainingService.method.getJob, { id }, { transport });
export const compareJobs = (ids: string[]) => createQueryOptions(TrainingService.method.compareJobs, { ids }, { transport });

export const listBenchmarks = createQueryOptions(BenchmarkService.method.listBenchmarks, {}, { transport });
export const getBenchmark = (id: string) => createQueryOptions(BenchmarkService.method.getBenchmark, { id }, { transport });

export const listScorings = createQueryOptions(ScoringService.method.listScorings, {}, { transport });
export const getScoring = (id: string) => createQueryOptions(ScoringService.method.getScoring, { id }, { transport });

// Keys for refreshing after a change.
export const allModelQueries = createConnectQueryKey({ schema: ModelService, cardinality: undefined });
export const allUserQueries = createConnectQueryKey({ schema: UserService, cardinality: undefined });
export const allKeyQueries = createConnectQueryKey({ schema: ApiKeyService, cardinality: undefined });
export const allAuthQueries = createConnectQueryKey({ schema: AuthService, cardinality: undefined });
export const allWebhookQueries = createConnectQueryKey({ schema: WebhookService, cardinality: undefined });
export const allSettingsQueries = createConnectQueryKey({ schema: SettingsService, cardinality: undefined });
export const allBenchmarkQueries = createConnectQueryKey({ schema: BenchmarkService, cardinality: undefined });
export const allScoringQueries = createConnectQueryKey({ schema: ScoringService, cardinality: undefined });
export const allDatasetQueries = createConnectQueryKey({ schema: DatasetService, cardinality: undefined });
export const listTeams = createQueryOptions(TeamService.method.listTeams, {}, { transport });
export const allTeamQueries = createConnectQueryKey({ schema: TeamService, cardinality: undefined });
export const usage = (month: string) => createQueryOptions(SettingsService.method.getUsage, { month }, { transport });
export const listWorkers = createQueryOptions(WorkerService.method.listWorkers, {}, { transport });
export const listWorkerChoices = createQueryOptions(WorkerService.method.listWorkerChoices, {}, { transport });
export const getWorker = (id: string, hours: number) => createQueryOptions(WorkerService.method.getWorker, { id, hours }, { transport });
export const cloudGpu = createQueryOptions(WorkerService.method.getCloudGpu, {}, { transport });
export const allWorkerQueries = createConnectQueryKey({ schema: WorkerService, cardinality: undefined });
export const allTrainingQueries = createConnectQueryKey({ schema: TrainingService, cardinality: undefined });
// The catalog only changes with the server, so it is fetched once.
export const listTemplates = { ...createQueryOptions(TemplateService.method.listTemplates, {}, { transport }), staleTime: Infinity };
export const signInOptions = createQueryOptions(SignInService.method.listSignInOptions, {}, { transport });
export const signInProviders = createQueryOptions(SignInService.method.listSignInProviders, {}, { transport });
export const allSignInQueries = createConnectQueryKey({ schema: SignInService, cardinality: undefined });
