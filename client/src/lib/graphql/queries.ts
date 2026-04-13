import { gql } from "graphql-request";

// Statistics
export const GET_STATISTICS = gql`
  query GetStatistics {
    statistics {
      recordCount
      actorCount
      lexiconCount
    }
  }
`;

// Settings
export const GET_SETTINGS = gql`
  query GetSettings {
    settings {
      domainAuthority
      adminDids
      relayUrl
      plcDirectoryUrl
      jetstreamUrl
      oauthSupportedScopes
    }
  }
`;

// Current Session
export const GET_CURRENT_SESSION = gql`
  query GetCurrentSession {
    currentSession {
      did
      handle
      isAdmin
    }
  }
`;

// Activity Buckets
export const GET_ACTIVITY_BUCKETS = gql`
  query GetActivityBuckets($range: TimeRange!) {
    activityBuckets(range: $range) {
      timestamp
      creates
      updates
      deletes
    }
  }
`;

// Recent Activity
export const GET_RECENT_ACTIVITY = gql`
  query GetRecentActivity($hours: Int!) {
    recentActivity(hours: $hours) {
      id
      timestamp
      operation
      collection
      did
      rkey
      status
      errorMessage
    }
  }
`;

// Validation Stats
export const GET_VALIDATION_STATS = gql`
  query GetValidationStats($range: TimeRange!) {
    validationStats(range: $range) {
      invalidCount
      invalidByCollection {
        collection
        count
      }
      recentInvalid {
        id
        timestamp
        operation
        collection
        did
        rkey
        status
        errorMessage
      }
      lastInvalidAt
    }
  }
`;

// Collection Overview
export const GET_COLLECTION_OVERVIEW = gql`
  query GetCollectionOverview {
    collectionOverview {
      collection
      recordCount
      invalidCount
    }
  }
`;

// Lexicons
export const GET_LEXICONS = gql`
  query GetLexicons {
    lexicons {
      id
      json
      createdAt
    }
  }
`;

// OAuth Clients
export const GET_OAUTH_CLIENTS = gql`
  query GetOAuthClients {
    oauthClients {
      clientId
      clientSecret
      clientName
      clientType
      redirectUris
      createdAt
    }
  }
`;

// Backfill Status
export const IS_BACKFILLING = gql`
  query IsBackfilling {
    isBackfilling
  }
`;
