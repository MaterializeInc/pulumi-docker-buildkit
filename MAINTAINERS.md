# Maintainer instructions

To release a new version:

1. Make changes.

2. Update changelog.

3. Commit changes and push to GitHub.

4. Create the tag.

    ```
    version=vA.B.C
    git tag -a $version -m $version
    git push
    git push origin $version
    ```
