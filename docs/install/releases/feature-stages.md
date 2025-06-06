# Feature stages

Some Coder features are released in feature stages before they are generally
available.

If you encounter an issue with any Coder feature, please submit a
[GitHub issue](https://github.com/coder/coder/issues) or join the
[Coder Discord](https://discord.gg/coder).

## Feature stages

| Feature stage                          | Stable | Production-ready | Support               | Description                                                                                                                   |
|----------------------------------------|--------|------------------|-----------------------|-------------------------------------------------------------------------------------------------------------------------------|
| [Early Access](#early-access-features) | No     | No               | GitHub issues         | For staging only. Not feature-complete or stable. Disabled by default.                                                        |
| [Beta](#beta)                          | No     | Not fully        | Docs, Discord, GitHub | Publicly available. In active development with minor bugs. Suitable for staging; optional for production. Not covered by SLA. |
| [GA](#general-availability-ga)         | Yes    | Yes              | License-based         | Stable and tested. Enabled by default. Fully documented. Support based on license.                                            |

## Early access features

- **Stable**: No
- **Production-ready**: No
- **Support**: GitHub issues

Early access features are neither feature-complete nor stable. We do not
recommend using early access features in production deployments.

Coder sometimes releases early access features that are available for use, but
are disabled by default. You shouldn't use early access features in production
because they might cause performance or stability issues. Early access features
can be mostly feature-complete, but require further internal testing and remain
in the early access stage for at least one month.

Coder may make significant changes or revert features to a feature flag at any
time.

If you plan to activate an early access feature, we suggest that you use a
staging deployment.

<details><summary>To enable early access features:</summary>

Use the [Coder CLI](../../install/cli.md) `--experiments` flag to enable early
access features:

- Enable all early access features:

  ```shell
  coder server --experiments=*
  ```

- Enable multiple early access features:

  ```shell
  coder server --experiments=feature1,feature2
  ```

You can also use the `CODER_EXPERIMENTS`
[environment variable](../../admin/setup/index.md).

You can opt-out of a feature after you've enabled it.

</details>

### Available early access features

<!-- Code generated by scripts/release/docs_update_experiments.sh. DO NOT EDIT. -->
<!-- BEGIN: available-experimental-features -->

| Feature               | Description                                  | Available in |
|-----------------------|----------------------------------------------|--------------|
| `workspace-prebuilds` | Enables the new workspace prebuilds feature. | mainline     |

<!-- END: available-experimental-features -->

## Beta

- **Stable**: No
- **Production-ready**: Not fully
- **Support**: Documentation, [Discord](https://discord.gg/coder), and
  [GitHub issues](https://github.com/coder/coder/issues)

Beta features are open to the public and are tagged with a `Beta` label.

They’re in active development and subject to minor changes. They might contain
minor bugs, but are generally ready for use.

Beta features are often ready for general availability within two-three
releases. You should test beta features in staging environments. You can use
beta features in production, but should set expectations and inform users that
some features may be incomplete.

We keep documentation about beta features up-to-date with the latest
information, including planned features, limitations, and workarounds. If you
encounter an issue, please contact your
[Coder account team](https://coder.com/contact), reach out on
[Discord](https://discord.gg/coder), or create a
[GitHub issues](https://github.com/coder/coder/issues) if there isn't one
already. While we will do our best to provide support with beta features, most
issues will be escalated to the product team. Beta features are not covered
within service-level agreements (SLA).

Most beta features are enabled by default. Beta features are announced through
the [Coder Changelog](https://coder.com/changelog), and more information is
available in the documentation.

## General Availability (GA)

- **Stable**: Yes
- **Production-ready**: Yes
- **Support**: Yes, [based on license](https://coder.com/pricing).

All features that are not explicitly tagged as `Early access` or `Beta` are
considered generally available (GA). They have been tested, are stable, and are
enabled by default.

If your Coder license includes an SLA, please consult it for an outline of
specific expectations.

For support, consult our knowledgeable and growing community on
[Discord](https://discord.gg/coder), or create a
[GitHub issue](https://github.com/coder/coder/issues) if one doesn't exist
already. Customers with a valid Coder license, can submit a support request or
contact your [account team](https://coder.com/contact).

We intend [Coder documentation](../../README.md) to be the
[single source of truth](https://en.wikipedia.org/wiki/Single_source_of_truth)
and all features should have some form of complete documentation that outlines
how to use or implement a feature. If you discover an error or if you have a
suggestion that could improve the documentation, please
[submit a GitHub issue](https://github.com/coder/internal/issues/new?title=request%28docs%29%3A+request+title+here&labels=["customer-feedback","docs"]&body=please+enter+your+request+here).

Some GA features can be disabled for air-gapped deployments. Consult the
feature's documentation or submit a support ticket for assistance.
