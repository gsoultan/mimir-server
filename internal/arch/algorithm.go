// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0 OR PolyForm-Small-Business-1.0.0 OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is free to use for personal and other noncommercial purposes, for
// nonprofits, and for companies with fewer than 100 people and under
// 1,000,000 USD in yearly revenue, under the PolyForm Noncommercial and
// PolyForm Small Business licenses. Any other use needs a commercial
// license from Gembit Soultan Shirazi. See the LICENSE and LICENSING.md files
// at the root of this repository.

package arch

// algorithms name the method each built-in model type learns with, in
// the words a data scientist would use, for people who want to know
// what runs underneath.
var algorithms = map[string]string{
	"mnist-mlp":         "Multilayer perceptron (a fully connected neural network) trained by gradient descent",
	"mnist-cnn":         "Convolutional neural network trained by gradient descent",
	"table-mlp":         "Multilayer perceptron over standardized columns and one-hot categories, trained by gradient descent",
	"text-mlp":          "Multilayer perceptron over hashed bag-of-words counts and the other columns, trained by gradient descent",
	"number-mlp":        "Multilayer perceptron regression, trained by gradient descent on squared error",
	"photo-cnn":         "Convolutional neural network trained by gradient descent",
	"photo-table":       "Convolutional neural network for the photo, joined with a multilayer perceptron for the columns",
	"table-auto":        "Automatic model selection: boosted trees, random forest, logistic regression, SVM, k-nearest neighbours and naive Bayes, each with several settings, scored on held-out rows",
	"number-auto":       "Automatic model selection: boosted trees, random forest, linear regression, SVM and k-nearest neighbours, each with several settings, scored on held-out rows",
	"table-boost":       "Gradient-boosted decision trees (as in XGBoost or LightGBM), with early stopping on held-out rows",
	"number-boost":      "Gradient-boosted regression trees (as in XGBoost or LightGBM), with early stopping on held-out rows",
	"table-forest":      "Random forest: bagged decision trees with random feature subsets",
	"number-forest":     "Random forest regression: bagged decision trees with random feature subsets",
	"table-linear":      "Logistic regression",
	"number-linear":     "Linear regression with a small L2 (ridge) penalty",
	"table-svm":         "Support vector machine with an RBF kernel",
	"number-svm":        "Support vector regression with an RBF kernel",
	"table-knn":         "k-nearest neighbours (k = 7), weighted by distance",
	"number-knn":        "k-nearest neighbours regression (k = 7), weighted by distance",
	"table-bayes":       "Naive Bayes",
	"find-groups":       "k-means clustering, with the number of groups picked by silhouette score",
	"odd-one":           "Isolation forest (anomaly detection)",
	"recommend":         "Matrix factorization for implicit feedback (collaborative filtering)",
	"recommend-nearby":  "Neighbourhood collaborative filtering: purchases of same-category customers within a haversine (great-circle) distance",
	"best-action":       "Contextual bandit by the direct method: gradient-boosted trees learn the outcome of each situation and action",
	"time-to-event":     "Discrete-time survival analysis with gradient-boosted trees, counting censored rows",
	"forecast":          "Gradient-boosted trees over lagged values and calendar features (direct multi-step forecasting)",
	"look-alike":        "Weighted k-nearest-neighbour similarity to the positive rows, with features weighted by how much they set them apart",
	"multi-label":       "One gradient-boosted tree classifier per label (binary relevance)",
	"discount-response": "Gradient-boosted tree classifier of the outcome given the situation and discount, swept over discount levels and kept non-decreasing",
	"number-range":      "Gradient-boosted regression trees with a second model of the error's size, calibrated by normalized conformal prediction",
	"find-duplicates":   "TF-IDF cosine similarity over the words of names and other details, ignoring case, word order and legal forms",
	"find-things":       "CenterNet object detection on a ResNet backbone, trained by gradient descent",
	"photo-resnet":      "ResNet-18 convolutional neural network, trained by gradient descent, optionally fine-tuned from another ResNet",
	"photo-mobilenet":   "MobileNet convolutional neural network (depthwise separable convolutions), trained by gradient descent",
	"sound":             "Convolutional neural network over a mel spectrogram of each second of sound",
	"language-model":    "Transformer language model, writing one token at a time",
	"mim":               "The model in the file, as it was built",
}

// Algorithm names the method a model type learns with, or "" when it is
// someone's own.
func (a Architecture) Algorithm() string { return algorithms[a.Name] }
