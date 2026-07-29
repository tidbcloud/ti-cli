package starter

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tidbcloud/tdc/internal/api"
)

const ProjectLabelKey = "tidb.cloud/project"

type Client struct {
	api *api.Client
}

func New(client *api.Client) *Client {
	return &Client{api: client}
}

type ListClustersOptions struct {
	PageSize  int32
	PageToken string
	Filter    string
	OrderBy   string
	Skip      int32
}

type ListClustersResponse struct {
	Clusters      []Cluster `json:"clusters"`
	NextPageToken string    `json:"next_page_token,omitempty"`
	TotalSize     int64     `json:"total_size,omitempty"`
}

type GetClusterOptions struct {
	View string
}

type CreateClusterRequest struct {
	DisplayName   string
	RegionName    string
	ProjectID     string
	SpendingLimit *SpendingLimit
}

type UpdateClusterRequest struct {
	DisplayName   *string
	SpendingLimit *SpendingLimit
	UpdateMask    []string
}

type Cluster struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name,omitempty"`
	DisplayName          string            `json:"display_name"`
	Region               Region            `json:"region"`
	SpendingLimit        *SpendingLimit    `json:"spending_limit,omitempty"`
	Endpoints            *Endpoints        `json:"endpoints,omitempty"`
	HighAvailabilityType string            `json:"high_availability_type,omitempty"`
	Version              string            `json:"version,omitempty"`
	CreatedBy            string            `json:"created_by,omitempty"`
	UserPrefix           string            `json:"user_prefix,omitempty"`
	State                string            `json:"state,omitempty"`
	Labels               map[string]string `json:"labels,omitempty"`
	Annotations          map[string]string `json:"annotations,omitempty"`
	CreateTime           string            `json:"create_time,omitempty"`
	UpdateTime           string            `json:"update_time,omitempty"`
	ClusterPlan          string            `json:"cluster_plan,omitempty"`
	AutoScaling          *AutoScaling      `json:"auto_scaling,omitempty"`
	ServicePlan          string            `json:"service_plan,omitempty"`
}

type Region struct {
	Name          string   `json:"name,omitempty"`
	RegionID      string   `json:"region_id,omitempty"`
	CloudProvider string   `json:"cloud_provider,omitempty"`
	DisplayName   string   `json:"display_name,omitempty"`
	Provider      string   `json:"provider,omitempty"`
	ServicePlans  []string `json:"service_plans,omitempty"`
}

type SpendingLimit struct {
	Monthly int32 `json:"monthly"`
}

type AutoScaling struct {
	MinRCU int64 `json:"min_rcu,omitempty"`
	MaxRCU int64 `json:"max_rcu,omitempty"`
}

type Endpoints struct {
	Public *PublicEndpoint `json:"public,omitempty"`
}

type PublicEndpoint struct {
	Host               string              `json:"host,omitempty"`
	Port               int32               `json:"port,omitempty"`
	Disabled           bool                `json:"disabled,omitempty"`
	AuthorizedNetworks []AuthorizedNetwork `json:"authorized_networks,omitempty"`
}

type AuthorizedNetwork struct {
	StartIPAddress string `json:"start_ip_address"`
	EndIPAddress   string `json:"end_ip_address"`
	DisplayName    string `json:"display_name"`
}

func (c *Client) ListClusters(ctx context.Context, opts ListClustersOptions) (ListClustersResponse, error) {
	requestPath := "/v1beta1/clusters"
	query := url.Values{}
	if opts.PageSize > 0 {
		query.Set("pageSize", strconv.FormatInt(int64(opts.PageSize), 10))
	}
	if opts.PageToken != "" {
		query.Set("pageToken", opts.PageToken)
	}
	if opts.Filter != "" {
		query.Set("filter", opts.Filter)
	}
	if opts.OrderBy != "" {
		query.Set("orderBy", opts.OrderBy)
	}
	if opts.Skip > 0 {
		query.Set("skip", strconv.FormatInt(int64(opts.Skip), 10))
	}
	if encoded := query.Encode(); encoded != "" {
		requestPath += "?" + encoded
	}

	req, err := c.api.NewRequest(ctx, http.MethodGet, requestPath, nil)
	if err != nil {
		return ListClustersResponse{}, err
	}
	var response listClustersWireResponse
	if err := c.api.DoJSON(req, &response); err != nil {
		return ListClustersResponse{}, err
	}
	return response.toResponse(), nil
}

func (c *Client) CreateCluster(ctx context.Context, input CreateClusterRequest) (Cluster, error) {
	var labels map[string]string
	if projectID := strings.TrimSpace(input.ProjectID); projectID != "" {
		labels = map[string]string{
			ProjectLabelKey: projectID,
		}
	}
	body := createClusterWireRequest{
		DisplayName: input.DisplayName,
		Region: &regionWire{
			Name: input.RegionName,
		},
		Labels:        labels,
		SpendingLimit: input.SpendingLimit,
	}
	req, err := c.api.NewRequest(ctx, http.MethodPost, "/v1beta1/clusters", body)
	if err != nil {
		return Cluster{}, err
	}
	var response clusterWire
	if err := c.api.DoJSON(req, &response); err != nil {
		return Cluster{}, err
	}
	return response.toCluster(), nil
}

func (c *Client) GetCluster(ctx context.Context, clusterID string, opts GetClusterOptions) (Cluster, error) {
	requestPath := "/v1beta1/clusters/" + url.PathEscape(clusterID)
	if opts.View != "" {
		query := url.Values{}
		query.Set("view", opts.View)
		requestPath += "?" + query.Encode()
	}
	req, err := c.api.NewRequest(ctx, http.MethodGet, requestPath, nil)
	if err != nil {
		return Cluster{}, err
	}
	var response clusterWire
	if err := c.api.DoJSON(req, &response); err != nil {
		return Cluster{}, err
	}
	return response.toCluster(), nil
}

func (c *Client) UpdateCluster(ctx context.Context, clusterID string, input UpdateClusterRequest) (Cluster, error) {
	body := updateClusterWireRequest{
		UpdateMask: strings.Join(input.UpdateMask, ","),
		Cluster: updateClusterFieldsWire{
			DisplayName:   input.DisplayName,
			SpendingLimit: input.SpendingLimit,
		},
	}
	req, err := c.api.NewRequest(ctx, http.MethodPatch, "/v1beta1/clusters/"+url.PathEscape(clusterID), body)
	if err != nil {
		return Cluster{}, err
	}
	var response clusterWire
	if err := c.api.DoJSON(req, &response); err != nil {
		return Cluster{}, err
	}
	return response.toCluster(), nil
}

func (c *Client) DeleteCluster(ctx context.Context, clusterID string) (Cluster, error) {
	req, err := c.api.NewRequest(ctx, http.MethodDelete, "/v1beta1/clusters/"+url.PathEscape(clusterID), nil)
	if err != nil {
		return Cluster{}, err
	}
	var response clusterWire
	if err := c.api.DoJSON(req, &response); err != nil {
		return Cluster{}, err
	}
	return response.toCluster(), nil
}

type listClustersWireResponse struct {
	Clusters         []clusterWire `json:"clusters"`
	NextPageToken    string        `json:"nextPageToken"`
	NextPageTokenAlt string        `json:"next_page_token"`
	TotalSize        int64         `json:"totalSize"`
	TotalSizeAlt     int64         `json:"total_size"`
}

func (r listClustersWireResponse) toResponse() ListClustersResponse {
	clusters := make([]Cluster, 0, len(r.Clusters))
	for _, cluster := range r.Clusters {
		clusters = append(clusters, cluster.toCluster())
	}
	nextPageToken := r.NextPageToken
	if nextPageToken == "" {
		nextPageToken = r.NextPageTokenAlt
	}
	totalSize := r.TotalSize
	if totalSize == 0 {
		totalSize = r.TotalSizeAlt
	}
	return ListClustersResponse{
		Clusters:      clusters,
		NextPageToken: nextPageToken,
		TotalSize:     totalSize,
	}
}

type clusterWire struct {
	Name                 string            `json:"name"`
	ClusterID            string            `json:"clusterId"`
	DisplayName          string            `json:"displayName"`
	Region               regionWire        `json:"region"`
	SpendingLimit        *SpendingLimit    `json:"spendingLimit"`
	Endpoints            *endpointsWire    `json:"endpoints"`
	HighAvailabilityType string            `json:"highAvailabilityType"`
	Version              string            `json:"version"`
	CreatedBy            string            `json:"createdBy"`
	UserPrefix           string            `json:"userPrefix"`
	State                string            `json:"state"`
	Labels               map[string]string `json:"labels"`
	Annotations          map[string]string `json:"annotations"`
	CreateTime           string            `json:"createTime"`
	UpdateTime           string            `json:"updateTime"`
	ClusterPlan          string            `json:"clusterPlan"`
	AutoScaling          *autoScalingWire  `json:"autoScaling"`
	ServicePlan          string            `json:"servicePlan"`
}

func (c clusterWire) toCluster() Cluster {
	id := c.ClusterID
	if id == "" {
		id = strings.TrimPrefix(c.Name, "clusters/")
	}
	return Cluster{
		ID:                   id,
		Name:                 c.Name,
		DisplayName:          c.DisplayName,
		Region:               c.Region.toRegion(),
		SpendingLimit:        c.SpendingLimit,
		Endpoints:            c.Endpoints.toEndpoints(),
		HighAvailabilityType: c.HighAvailabilityType,
		Version:              c.Version,
		CreatedBy:            c.CreatedBy,
		UserPrefix:           c.UserPrefix,
		State:                c.State,
		Labels:               c.Labels,
		Annotations:          c.Annotations,
		CreateTime:           c.CreateTime,
		UpdateTime:           c.UpdateTime,
		ClusterPlan:          c.ClusterPlan,
		AutoScaling:          c.AutoScaling.toAutoScaling(),
		ServicePlan:          c.ServicePlan,
	}
}

type regionWire struct {
	Name          string   `json:"name,omitempty"`
	RegionID      string   `json:"regionId,omitempty"`
	CloudProvider string   `json:"cloudProvider,omitempty"`
	DisplayName   string   `json:"displayName,omitempty"`
	Provider      string   `json:"provider,omitempty"`
	ServicePlans  []string `json:"servicePlans,omitempty"`
}

func (r regionWire) toRegion() Region {
	return Region{
		Name:          r.Name,
		RegionID:      r.RegionID,
		CloudProvider: r.CloudProvider,
		DisplayName:   r.DisplayName,
		Provider:      r.Provider,
		ServicePlans:  r.ServicePlans,
	}
}

type autoScalingWire struct {
	MinRCU int64 `json:"minRcu,omitempty"`
	MaxRCU int64 `json:"maxRcu,omitempty"`
}

func (a *autoScalingWire) toAutoScaling() *AutoScaling {
	if a == nil {
		return nil
	}
	return &AutoScaling{
		MinRCU: a.MinRCU,
		MaxRCU: a.MaxRCU,
	}
}

type endpointsWire struct {
	Public *publicEndpointWire `json:"public,omitempty"`
}

func (e *endpointsWire) toEndpoints() *Endpoints {
	if e == nil {
		return nil
	}
	return &Endpoints{
		Public: e.Public.toPublicEndpoint(),
	}
}

type publicEndpointWire struct {
	Host               string                  `json:"host,omitempty"`
	Port               int32                   `json:"port,omitempty"`
	Disabled           bool                    `json:"disabled,omitempty"`
	AuthorizedNetworks []authorizedNetworkWire `json:"authorizedNetworks,omitempty"`
}

func (e *publicEndpointWire) toPublicEndpoint() *PublicEndpoint {
	if e == nil {
		return nil
	}
	networks := make([]AuthorizedNetwork, 0, len(e.AuthorizedNetworks))
	for _, network := range e.AuthorizedNetworks {
		networks = append(networks, AuthorizedNetwork{
			StartIPAddress: network.StartIPAddress,
			EndIPAddress:   network.EndIPAddress,
			DisplayName:    network.DisplayName,
		})
	}
	return &PublicEndpoint{
		Host:               e.Host,
		Port:               e.Port,
		Disabled:           e.Disabled,
		AuthorizedNetworks: networks,
	}
}

type authorizedNetworkWire struct {
	StartIPAddress string `json:"startIpAddress"`
	EndIPAddress   string `json:"endIpAddress"`
	DisplayName    string `json:"displayName"`
}

type createClusterWireRequest struct {
	DisplayName   string            `json:"displayName"`
	Region        *regionWire       `json:"region,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	SpendingLimit *SpendingLimit    `json:"spendingLimit,omitempty"`
}

type updateClusterWireRequest struct {
	Cluster    updateClusterFieldsWire `json:"cluster"`
	UpdateMask string                  `json:"updateMask"`
}

type updateClusterFieldsWire struct {
	DisplayName   *string        `json:"displayName,omitempty"`
	SpendingLimit *SpendingLimit `json:"spendingLimit,omitempty"`
}
