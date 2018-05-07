pipeline {
    agent any

    environment {
        APP_NAME = 'sirdata-monitoring'
	KUBE_CREDS = credentials('kube-creds')
    }

    parameters {
        string(name: 'VERSION', defaultValue: BUILD_NUMBER, description: 'Which version to build ?')
        string(name: 'ENV', defaultValue: 'preprod', description: 'On which environment to deploy ?')
	string(name: 'OPTIONAL_ARGS', defaultValue: '', description: 'optional arguments to pass to build script')
    }

    stages {
        stage('Build and Deploy') {
            steps {
		echo "App: ${APP_NAME}"
		echo "Env: ${params.ENV}"
		echo "Version: ${params.VERSION}"
		wrap([$class: 'BuildUser']) {
			sh "/usr/local/admin-scripts/shellscripts/update-kubernetes-image.sh -p ${APP_NAME} -e ${params.ENV} -v ${params.VERSION} -u ${BUILD_USER_ID} ${params.OPTIONAL_ARGS}"
		}
            }
        }
    }

    post {
        always {
            echo 'Build Finished'
        }
            
        success {
		wrap([$class: 'BuildUser']) {
			slackSend color: 'good', message: "Build for project \"${APP_NAME}\" version \"${VERSION}\" on env \"${ENV}\" by ${BUILD_USER_ID} Succeded !"
		}
		echo 'Build Succeded !"'
        }
            
        failure {
		wrap([$class: 'BuildUser']) {
			slackSend color: 'bad', message: "Build for project \"${APP_NAME}\" version \"${VERSION}\" on env \"${ENV}\" by ${BUILD_USER_ID} Failed !"
		}
		echo 'Build Failed !"'
        }
    }
}
