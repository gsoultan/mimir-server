import { createConnectQueryKey, createInfiniteQueryOptions, createQueryOptions } from "@connectrpc/connect-query";

import { ApiKeyService, AuditService, AuthService, UserService } from "./gen/mimir/v1/access_pb";
import { ModelService } from "./gen/mimir/v1/model_pb";
import { transport } from "./client";

export const listModels = createQueryOptions(ModelService.method.listModels, {}, { transport });
export const listArchitectures = createQueryOptions(ModelService.method.listArchitectures, {}, { transport });
export const getModel = (id: string) => createQueryOptions(ModelService.method.getModel, { id }, { transport });

export const setupState = createQueryOptions(AuthService.method.getSetupState, {}, { transport });
export const whoAmI = createQueryOptions(AuthService.method.whoAmI, {}, { transport });
export const listUsers = createQueryOptions(UserService.method.listUsers, {}, { transport });
export const listApiKeys = createQueryOptions(ApiKeyService.method.listApiKeys, {}, { transport });
export const auditLog = createInfiniteQueryOptions(AuditService.method.listAuditEvents, { pageSize: 50, pageToken: "" }, {
  transport,
  pageParamKey: "pageToken",
  getNextPageParam: (last) => last.nextPageToken || undefined,
});

// Keys for refreshing after a change.
export const allModelQueries = createConnectQueryKey({ schema: ModelService, cardinality: undefined });
export const allUserQueries = createConnectQueryKey({ schema: UserService, cardinality: undefined });
export const allKeyQueries = createConnectQueryKey({ schema: ApiKeyService, cardinality: undefined });
export const allAuthQueries = createConnectQueryKey({ schema: AuthService, cardinality: undefined });
