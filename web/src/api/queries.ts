import { createConnectQueryKey, createQueryOptions } from "@connectrpc/connect-query";

import { ModelService } from "./gen/mimir/v1/model_pb";
import { transport } from "./client";

export const listModels = createQueryOptions(ModelService.method.listModels, {}, { transport });
export const listArchitectures = createQueryOptions(ModelService.method.listArchitectures, {}, { transport });
export const getModel = (id: string) => createQueryOptions(ModelService.method.getModel, { id }, { transport });

// Every model query, for refreshing after a change.
export const allModelQueries = createConnectQueryKey({ schema: ModelService, cardinality: undefined });
